// Package resolve computes effective access from a collected snapshot.
//
// Everything here is pure: it takes a kc.Snapshot and returns grants, performing
// no I/O. That is deliberate — the correctness bar for an access review is this
// package, so it must be testable by constructing a snapshot literal rather than
// by standing up a server.
//
// Three things make effective access different from the direct role mappings the
// Admin API returns, and getting any of them wrong produces output that looks
// complete and is wrong:
//
//  1. Group roles are inherited from ANCESTOR groups, not only the group joined.
//  2. Composite roles nest recursively, and cross the realm/client boundary.
//  3. A review built on direct mappings alone is not slightly incomplete; it is
//     wrong evidence, and neither reviewer nor auditor can tell.
package resolve

import (
	"cmp"
	"strings"

	"github.com/softika/kcac/internal/model"
)

// PathKind classifies how a grant reaches a user, for filtering and reporting.
type PathKind string

const (
	// PathDirect is a role assigned straight to the user.
	PathDirect PathKind = "direct"
	// PathGroup is a role granted by a group the user belongs to, or by one of
	// that group's ancestors.
	PathGroup PathKind = "group"
	// PathComposite is a role reached by expanding a directly assigned composite.
	PathComposite PathKind = "composite"
	// PathGroupComposite is a role reached by expanding a composite that a group
	// granted.
	PathGroupComposite PathKind = "group+composite"
)

// Path is one route by which a user holds an entitlement.
//
// A user can hold the same entitlement several ways; each route is a separate
// Path, and the reviewer-facing rendering names every hop so the grant can
// actually be judged rather than rubber-stamped.
type Path struct {
	// MemberGroup is the group the user belongs to. Empty for a direct
	// assignment.
	MemberGroup string
	// GrantGroup is the group the role is granted on. It differs from
	// MemberGroup when the role is inherited from an ancestor — the case a
	// hand-rolled export misses.
	GrantGroup string
	// Chain runs from the originally granted role to the entitlement. A chain of
	// length one means no composite expansion happened.
	Chain []model.RoleKey
}

// Kind classifies the path.
func (p Path) Kind() PathKind {
	composite := len(p.Chain) > 1
	switch {
	case p.MemberGroup == "" && !composite:
		return PathDirect
	case p.MemberGroup == "" && composite:
		return PathComposite
	case !composite:
		return PathGroup
	default:
		return PathGroupComposite
	}
}

// String renders the path for a reviewer. This is the grant_path column, and it
// is the single most important value kcac produces.
func (p Path) String() string {
	var b strings.Builder

	if p.MemberGroup == "" {
		b.WriteString("direct assignment")
		if len(p.Chain) > 1 {
			// Name the assigned role, then the expansion that followed from it.
			b.WriteString(" of ")
			b.WriteString(displayRole(p.Chain[0]))
			b.WriteString(" → composite ")
			b.WriteString(joinRoles(p.Chain[1:]))
		}
		return b.String()
	}

	b.WriteString("via group ")
	b.WriteString(p.MemberGroup)
	if p.GrantGroup != "" && p.GrantGroup != p.MemberGroup {
		b.WriteString(" (inherited from ")
		b.WriteString(p.GrantGroup)
		b.WriteString(")")
	}
	if len(p.Chain) > 1 {
		// No role is named in the prefix, so the whole chain follows.
		b.WriteString(" → composite ")
		b.WriteString(joinRoles(p.Chain))
	}
	return b.String()
}

// cost ranks paths so the simplest true explanation is the one a reviewer sees
// first. Fewer composite hops wins; a direct assignment beats a group grant; and
// an inherited grant is one step further removed than a grant on the user's own
// group.
func (p Path) cost() int {
	c := len(p.Chain) - 1
	if p.MemberGroup != "" {
		c++
		if p.GrantGroup != "" && p.GrantGroup != p.MemberGroup {
			c++
		}
	}
	return c
}

// compare orders paths so the simplest true explanation sorts first. Ties break
// on the rendered string, so output never depends on map ordering.
func (p Path) compare(other Path) int {
	if c := cmp.Compare(p.cost(), other.cost()); c != 0 {
		return c
	}
	return cmp.Compare(p.String(), other.String())
}

// displayRole renders a role the way a reviewer reads it: a bare name for a
// realm role, and clientId-qualified for a client role. Never an id.
func displayRole(k model.RoleKey) string {
	if k.IsClientRole() {
		return k.Client + ":" + k.Name
	}
	return k.Name
}

func joinRoles(keys []model.RoleKey) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, displayRole(k))
	}
	return strings.Join(parts, " → ")
}
