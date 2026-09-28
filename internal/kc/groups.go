package kc

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/softika/kcac/internal/model"
)

// maxGroupDepth guards the tree walk. Keycloak does not permit cycles in the
// group hierarchy, but kcac refuses to hang on a malformed or hostile response.
const maxGroupDepth = 100

// childrenSupport records whether this server exposes
// GET /admin/realms/{realm}/groups/{id}/children, added in Keycloak 23.0.0.
//
// kcac detects the capability by probing once and caching the answer, rather
// than parsing a version string: /admin/serverinfo needs privileges beyond the
// documented read-only set, so version may simply be unavailable.
type childrenSupport int

const (
	childrenUnknown childrenSupport = iota
	childrenSupported
	childrenUnsupported
)

// GroupTree returns every group in the realm as a flat slice with ParentID and
// Path authoritative and SubGroups cleared.
//
// Two server shapes are supported. From Keycloak 23 the top-level /groups
// response omits descendants and children come from /groups/{id}/children. Before
// 23 the whole tree arrives nested in /groups and /children does not exist. An
// empty SubGroups therefore means "unknown", never "no children", which is why
// the capability is probed rather than inferred.
func (c *Client) GroupTree(ctx context.Context) ([]model.GroupRepresentation, error) {
	roots, err := paged[model.GroupRepresentation](ctx, c, c.cfg.adminPath("/groups"), briefFalse())
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}

	w := &groupWalker{client: c, seen: make(map[string]bool)}
	if err := w.walk(ctx, roots, "", "", 0); err != nil {
		return nil, err
	}
	return w.flat, nil
}

type groupWalker struct {
	client *Client
	flat   []model.GroupRepresentation
	seen   map[string]bool
}

func (w *groupWalker) walk(ctx context.Context, groups []model.GroupRepresentation, parentID, parentPath string, depth int) error {
	if depth > maxGroupDepth {
		return fmt.Errorf("group hierarchy deeper than %d levels below %q: refusing to continue", maxGroupDepth, parentPath)
	}

	for _, g := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		if g.ID == "" {
			continue
		}
		// Defensive: a repeated id would otherwise duplicate a whole subtree,
		// inflating every grant derived from it.
		if w.seen[g.ID] {
			continue
		}
		w.seen[g.ID] = true

		// Build a normalised copy; the fetched representation is not mutated.
		node := model.GroupRepresentation{
			ID:       g.ID,
			Name:     g.Name,
			Path:     groupPath(g, parentPath),
			ParentID: parentID,
		}
		w.flat = append(w.flat, node)

		children, err := w.childrenOf(ctx, g)
		if err != nil {
			return err
		}
		if err := w.walk(ctx, children, node.ID, node.Path, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// childrenOf returns a group's direct children from whichever source this server
// supports.
func (w *groupWalker) childrenOf(ctx context.Context, g model.GroupRepresentation) ([]model.GroupRepresentation, error) {
	switch w.client.childrenMode() {
	case childrenUnsupported:
		return g.SubGroups, nil

	case childrenSupported:
		return w.client.groupChildren(ctx, g.ID)

	default: // childrenUnknown — probe once and cache the answer.
		children, err := w.client.groupChildren(ctx, g.ID)
		if err != nil {
			if IsEndpointAbsent(err) {
				w.client.setChildrenMode(childrenUnsupported)
				return g.SubGroups, nil
			}
			return nil, err
		}
		w.client.setChildrenMode(childrenSupported)
		return children, nil
	}
}

// groupChildren fetches one group's direct children.
//
// The explicit max from paged matters more here than anywhere else: this
// endpoint's server-side default is 10, so an implementation that omits it
// silently truncates every group with more than ten children.
func (c *Client) groupChildren(ctx context.Context, groupID string) ([]model.GroupRepresentation, error) {
	children, err := paged[model.GroupRepresentation](ctx, c,
		c.cfg.adminPath("/groups/%s/children", url.PathEscape(groupID)), briefFalse())
	if err != nil {
		return nil, err
	}
	return children, nil
}

// groupPath returns the group's path, deriving it from the parent when the
// server omits it. A path is how a reviewer recognises a group, so kcac never
// emits an empty one.
func groupPath(g model.GroupRepresentation, parentPath string) string {
	if g.Path != "" {
		return g.Path
	}
	return strings.TrimRight(parentPath, "/") + "/" + g.Name
}

// childrenModeState is embedded in Client to cache capability detection.
type childrenModeState struct {
	mu   sync.Mutex
	mode childrenSupport
}

func (c *Client) childrenMode() childrenSupport {
	c.children.mu.Lock()
	defer c.children.mu.Unlock()
	return c.children.mode
}

func (c *Client) setChildrenMode(mode childrenSupport) {
	c.children.mu.Lock()
	defer c.children.mu.Unlock()
	c.children.mode = mode
}

// SupportsGroupChildren reports what capability detection concluded, for the run
// manifest. It is meaningful only after GroupTree has run.
func (c *Client) SupportsGroupChildren() (known, supported bool) {
	switch c.childrenMode() {
	case childrenSupported:
		return true, true
	case childrenUnsupported:
		return true, false
	default:
		return false, false
	}
}
