package resolve

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/model"
)

// maxAncestorDepth guards the parent walk against a malformed tree. Keycloak
// cannot produce a cycle in group parentage, but kcac never hangs on data it did
// not create.
const maxAncestorDepth = 200

// groupGrant is a role a group confers, together with the group it is actually
// granted on. Those differ whenever the role comes from an ancestor, and the
// distinction is what lets a reviewer see why a grant exists.
type groupGrant struct {
	Role       model.RoleKey
	GrantGroup string
}

// groupIndex answers "what does membership of this group confer" by walking up
// the tree.
//
// This is correctness case #1. Keycloak resolves it server-side in
// RoleUtils.addGroupRoles, which recurses group.getParent(); a member of
// /A/B/C therefore holds every role granted on /A/B and /A as well. Note this
// differs from the groups token claim, which is why the behaviour is easy to
// miss and expensive to get wrong.
type groupIndex struct {
	byID  map[string]model.GroupRepresentation
	roles map[string][]model.RoleKey
	memo  map[string][]groupGrant
	warn  map[string]struct{}
}

func newGroupIndex(snap *kc.Snapshot) *groupIndex {
	idx := &groupIndex{
		byID:  make(map[string]model.GroupRepresentation, len(snap.Groups)),
		roles: snap.GroupRoles,
		memo:  make(map[string][]groupGrant, len(snap.Groups)),
		warn:  make(map[string]struct{}),
	}
	for _, g := range snap.Groups {
		idx.byID[g.ID] = g
	}
	return idx
}

// group returns a group by id, recording a warning if the snapshot references
// one that is not in the tree.
func (idx *groupIndex) group(id string) (model.GroupRepresentation, bool) {
	g, ok := idx.byID[id]
	if !ok {
		idx.warn[fmt.Sprintf("snapshot references unknown group %q: it may have been deleted during collection, so some access may be unreported", id)] = struct{}{}
	}
	return g, ok
}

// grants returns every role conferred by membership of groupID: the group's own
// grants plus every ancestor's, each tagged with where it is granted.
func (idx *groupIndex) grants(groupID string) []groupGrant {
	if cached, ok := idx.memo[groupID]; ok {
		return cached
	}

	var (
		out  []groupGrant
		seen = make(map[string]bool) // guards against malformed parent links
	)

	for id, depth := groupID, 0; id != ""; depth++ {
		// Stopping here drops the remaining ancestors' grants, which under-reports
		// access. Surfacing exactly this kind of limit is the point of the tool,
		// so it cannot be silent. Not reachable while kc.maxGroupDepth is lower
		// than this cap, but the two constants live in different packages and can
		// drift apart.
		if depth >= maxAncestorDepth {
			idx.warn[fmt.Sprintf("group %q has more than %d ancestors; roles granted above that depth are not reported", groupID, maxAncestorDepth)] = struct{}{}
			break
		}
		if seen[id] {
			idx.warn[fmt.Sprintf("group parent chain from %q loops back on itself; ancestor walk stopped", groupID)] = struct{}{}
			break
		}
		seen[id] = true

		g, ok := idx.byID[id]
		if !ok {
			break
		}

		roles := slices.Clone(idx.roles[id])
		slices.SortFunc(roles, func(a, b model.RoleKey) int { return cmp.Compare(a.String(), b.String()) })
		for _, r := range roles {
			out = append(out, groupGrant{Role: r, GrantGroup: g.Path})
		}

		id = g.ParentID
	}

	idx.memo[groupID] = out
	return out
}

// warnings returns anything that limits confidence in the group data.
func (idx *groupIndex) warnings() []string {
	out := make([]string, 0, len(idx.warn))
	for w := range idx.warn {
		out = append(out, w)
	}
	slices.Sort(out)
	return out
}
