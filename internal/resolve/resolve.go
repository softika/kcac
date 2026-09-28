package resolve

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/model"
)

// Options tunes resolution.
type Options struct {
	// ExcludeDefaultRoles drops Keycloak's default-roles-<realm> composite as a
	// path origin, and with it everything reachable only through that role
	// (offline_access, uma_authorization, the account client roles).
	//
	// Every user holds it, so including it adds several rows per user that no
	// reviewer will ever act on. Excluding it is a real loss of completeness
	// though, so it is opt-in and recorded in the result warnings rather than
	// applied quietly.
	ExcludeDefaultRoles bool
}

// Grant is one user holding one entitlement, with the provenance a reviewer
// needs in order to judge it.
type Grant struct {
	UserID   string
	Username string
	Email    string
	// Enabled reports the account state. A disabled account still holds standing
	// access and still needs reviewing, so it is reported rather than filtered.
	Enabled bool

	Entitlement model.RoleKey

	// Path is the simplest true explanation of this grant, the one rendered in
	// the grant_path column.
	Path Path
	// PathCount is how many distinct routes confer this entitlement.
	PathCount int
	// AllPaths is every route, shortest first. The explain command shows these.
	AllPaths []Path
}

// Result is the resolved access picture for one realm.
type Result struct {
	Realm      string
	ResolvedAt time.Time

	// Grants is sorted by username then entitlement, so two review cycles diff
	// cleanly against each other.
	Grants []Grant

	// Users is every account considered, sorted by username.
	//
	// Grant carries only the identity fields it needs, so classification data
	// such as FederationLink and ServiceAccountClientLink lives here. Rendering
	// account_class from a Grant alone would silently mislabel every federated
	// account as an ordinary one.
	Users []model.UserRepresentation

	// UsersWithoutGrants are accounts that hold no access at all. They are
	// reported rather than omitted: an account nobody has judged is a review
	// finding, and silence looks identical to absence of data.
	UsersWithoutGrants []model.UserRepresentation

	// Warnings lists everything limiting the completeness of this result.
	Warnings []string
}

// Resolve computes effective access from a snapshot. It never returns nil.
func Resolve(snap *kc.Snapshot, opts Options) *Result {
	res := &Result{ResolvedAt: time.Now().UTC()}
	if snap == nil {
		return res
	}
	res.Realm = snap.Realm

	idx := newGroupIndex(snap)
	exp := newExpander(snap.Composites)
	defaultRole := model.RealmRole("default-roles-" + snap.Realm)

	users := sortedUsers(snap.Users)
	res.Users = users
	for _, u := range users {
		routes := collectRoutes(snap, idx, exp, u, opts, defaultRole)
		if len(routes) == 0 {
			res.UsersWithoutGrants = append(res.UsersWithoutGrants, u)
			continue
		}
		res.Grants = append(res.Grants, grantsFrom(u, routes)...)
	}

	res.Warnings = append(res.Warnings, exp.warnings()...)
	res.Warnings = append(res.Warnings, idx.warnings()...)
	if opts.ExcludeDefaultRoles {
		res.Warnings = append(res.Warnings,
			"default-roles-"+snap.Realm+" was excluded: access conferred only through it is not reported")
	}
	slices.Sort(res.Warnings)

	return res
}

// collectRoutes gathers every route from one user to every entitlement they hold.
func collectRoutes(snap *kc.Snapshot, idx *groupIndex, exp *expander,
	u model.UserRepresentation, opts Options, defaultRole model.RoleKey) map[model.RoleKey][]Path {

	routes := make(map[model.RoleKey][]Path)

	// Direct role assignments, expanded through the composite graph.
	for _, direct := range sortedRoles(snap.UserRoles[u.ID]) {
		if opts.ExcludeDefaultRoles && direct == defaultRole {
			continue
		}
		for _, r := range exp.closure(direct) {
			routes[r.Role] = append(routes[r.Role], Path{Chain: r.Chain})
		}
	}

	// Group memberships. Each contributes the group's own grants AND every
	// ancestor's, then each of those expands through composites.
	for _, groupID := range sortedGroupIDs(snap.UserGroupIDs[u.ID], idx) {
		g, ok := idx.group(groupID)
		if !ok {
			continue
		}
		for _, gg := range idx.grants(groupID) {
			if opts.ExcludeDefaultRoles && gg.Role == defaultRole {
				continue
			}
			for _, r := range exp.closure(gg.Role) {
				routes[r.Role] = append(routes[r.Role], Path{
					MemberGroup: g.Path,
					GrantGroup:  gg.GrantGroup,
					Chain:       r.Chain,
				})
			}
		}
	}

	return routes
}

// grantsFrom turns one user's routes into sorted grants, choosing the simplest
// explanation for each entitlement.
func grantsFrom(u model.UserRepresentation, routes map[model.RoleKey][]Path) []Grant {
	// The rendered form is computed once per entitlement rather than inside the
	// comparator: String() allocates, and a comparator runs O(n log n) times.
	type sortable struct {
		key     model.RoleKey
		ordered string
	}
	keys := make([]sortable, 0, len(routes))
	for k := range routes {
		keys = append(keys, sortable{key: k, ordered: k.String()})
	}
	slices.SortFunc(keys, func(a, b sortable) int { return cmp.Compare(a.ordered, b.ordered) })

	grants := make([]Grant, 0, len(keys))
	for _, k := range keys {
		key := k.key
		paths := dedupePaths(routes[key])
		slices.SortFunc(paths, Path.compare)

		grants = append(grants, Grant{
			UserID:      u.ID,
			Username:    u.Username,
			Email:       u.Email,
			Enabled:     u.Enabled,
			Entitlement: key,
			Path:        paths[0],
			PathCount:   len(paths),
			AllPaths:    paths,
		})
	}
	return grants
}

// dedupePaths removes identical routes, which arise when an ancestor grants a
// role that a descendant also grants. Counting those twice would overstate how
// many independent reasons a grant has.
func dedupePaths(paths []Path) []Path {
	seen := make(map[string]bool, len(paths))
	out := make([]Path, 0, len(paths))
	for _, p := range paths {
		key := p.MemberGroup + "\x00" + p.GrantGroup + "\x00" + joinRoles(p.Chain)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

func sortedUsers(users []model.UserRepresentation) []model.UserRepresentation {
	out := make([]model.UserRepresentation, len(users))
	copy(out, users)
	slices.SortFunc(out, func(a, b model.UserRepresentation) int {
		return cmp.Or(
			cmp.Compare(a.Username, b.Username),
			cmp.Compare(a.ID, b.ID),
		)
	})
	return out
}

func sortedRoles(roles []model.RoleKey) []model.RoleKey {
	out := make([]model.RoleKey, len(roles))
	copy(out, roles)
	slices.SortFunc(out, func(a, b model.RoleKey) int { return cmp.Compare(a.String(), b.String()) })
	return out
}

// sortedGroupIDs orders memberships by group path so output is stable, falling
// back to the id for groups missing from the tree.
func sortedGroupIDs(ids []string, idx *groupIndex) []string {
	out := make([]string, len(ids))
	copy(out, ids)
	slices.SortFunc(out, func(a, b string) int {
		return cmp.Or(
			cmp.Compare(idx.byID[a].Path, idx.byID[b].Path),
			cmp.Compare(a, b),
		)
	})
	return out
}

// Stats summarises a result for progress output and the run manifest.
func (r *Result) Stats() string {
	s := fmt.Sprintf("%d grants across %d users", len(r.Grants), r.userCount())
	if n := len(r.UsersWithoutGrants); n > 0 {
		s += fmt.Sprintf(" (%d with no access)", n)
	}
	return s
}

func (r *Result) userCount() int {
	seen := make(map[string]struct{}, len(r.Grants))
	for _, g := range r.Grants {
		seen[g.UserID] = struct{}{}
	}
	return len(seen) + len(r.UsersWithoutGrants)
}
