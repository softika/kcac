package resolve_test

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/model"
	"github.com/softika/kcac/internal/resolve"
)

// entitlements returns the entitlement keys resolved for a user, sorted.
func entitlements(t *testing.T, res *resolve.Result, username string) []string {
	t.Helper()
	var out []string
	for _, g := range res.Grants {
		if g.Username == username {
			out = append(out, g.Entitlement.String())
		}
	}
	sort.Strings(out)
	return out
}

// grantFor returns the single grant of one entitlement for one user.
func grantFor(t *testing.T, res *resolve.Result, username, entitlement string) resolve.Grant {
	t.Helper()
	for _, g := range res.Grants {
		if g.Username == username && g.Entitlement.String() == entitlement {
			return g
		}
	}
	t.Fatalf("no grant of %s for %s; resolved: %v", entitlement, username, entitlements(t, res, username))
	return resolve.Grant{}
}

// naiveDirectOnly is the export a hand-rolled script produces: direct role
// mappings, no group inheritance, no composite expansion. Used to prove the
// difference is real rather than theoretical.
func naiveDirectOnly(snap *kc.Snapshot, username string) []string {
	for _, u := range snap.Users {
		if u.Username != username {
			continue
		}
		var out []string
		for _, r := range snap.UserRoles[u.ID] {
			out = append(out, r.String())
		}
		sort.Strings(out)
		return out
	}
	return nil
}

// ---------------------------------------------------------------------------
// Correctness case #1 — group roles are inherited from ANCESTORS
// ---------------------------------------------------------------------------

func TestCase1_RoleFromParentGroupIsInherited(t *testing.T) {
	// a.gruber belongs only to /Retail/StoreManagers. store-admin is granted
	// there, but base-employee is granted on the PARENT /Retail. Keycloak grants
	// both, because RoleUtils.addGroupRoles walks getParent() recursively.
	snap := retailRealm().
		user("a.gruber", []string{"/Retail/StoreManagers"}).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	got := entitlements(t, res, "a.gruber")
	want := []string{
		"client:pos-app:till-operator", // via store-admin composite
		"realm:base-employee",          // granted on the PARENT group
		"realm:store-admin",            // granted on the member group
		"realm:till-supervisor",        // via store-admin composite
	}
	if !slices.Equal(got, want) {
		t.Errorf("entitlements = %v\nwant %v", got, want)
	}

	// The inherited one must say where it actually comes from, or the reviewer
	// cannot judge it.
	g := grantFor(t, res, "a.gruber", "realm:base-employee")
	if g.Path.GrantGroup != "/Retail" {
		t.Errorf("base-employee GrantGroup = %q, want /Retail", g.Path.GrantGroup)
	}
	if g.Path.MemberGroup != "/Retail/StoreManagers" {
		t.Errorf("base-employee MemberGroup = %q, want /Retail/StoreManagers", g.Path.MemberGroup)
	}
	if !strings.Contains(g.Path.String(), "/Retail") {
		t.Errorf("grant_path %q should name the ancestor the role comes from", g.Path)
	}
}

func TestCase1_ThreeLevelsOfInheritance(t *testing.T) {
	// s.novak is in the deepest group and must collect roles from all three
	// levels plus every composite they open up.
	snap := retailRealm().
		user("s.novak", []string{"/Retail/StoreManagers/Region-East"}).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	got := entitlements(t, res, "s.novak")
	want := []string{
		"client:pos-app:till-admin",    // own group
		"client:pos-app:till-operator", // via till-admin AND via store-admin
		"realm:base-employee",          // grandparent /Retail
		"realm:store-admin",            // parent /Retail/StoreManagers
		"realm:till-supervisor",        // via store-admin composite
	}
	if !slices.Equal(got, want) {
		t.Errorf("entitlements = %v\nwant %v", got, want)
	}
}

func TestCase1_SiblingGroupGrantsNothingExtra(t *testing.T) {
	// Region-West has no roles of its own; its member still inherits ancestors,
	// and must NOT pick up Region-East's client role.
	snap := retailRealm().
		user("w.west", []string{"/Retail/StoreManagers/Region-West"}).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	got := entitlements(t, res, "w.west")
	if slices.Contains(got, "client:pos-app:till-admin") {
		t.Errorf("Region-West member picked up a sibling group's role: %v", got)
	}
	for _, want := range []string{"realm:base-employee", "realm:store-admin"} {
		if !slices.Contains(got, want) {
			t.Errorf("entitlements %v missing inherited %s", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Correctness case #2 — composites nest and cross the realm/client boundary
// ---------------------------------------------------------------------------

func TestCase2_CompositeChainExpandsFully(t *testing.T) {
	// One direct role at the top of a three-hop chain must yield everything
	// below it: regional-manager -> store-admin -> till-supervisor -> base-employee.
	snap := retailRealm().
		user("r.chief", nil, model.RealmRole("regional-manager")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	got := entitlements(t, res, "r.chief")
	want := []string{
		"client:pos-app:till-operator",
		"realm:base-employee",
		"realm:regional-manager",
		"realm:store-admin",
		"realm:till-supervisor",
	}
	if !slices.Equal(got, want) {
		t.Errorf("entitlements = %v\nwant %v", got, want)
	}

	// The deepest leaf must carry the full derivation.
	g := grantFor(t, res, "r.chief", "realm:base-employee")
	chain := make([]string, 0, len(g.Path.Chain))
	for _, k := range g.Path.Chain {
		chain = append(chain, k.Name)
	}
	wantChain := []string{"regional-manager", "store-admin", "till-supervisor", "base-employee"}
	if !slices.Equal(chain, wantChain) {
		t.Errorf("chain = %v, want %v", chain, wantChain)
	}
}

func TestCase2_ClientRoleNestedInsideRealmComposite(t *testing.T) {
	// store-admin is a REALM role containing a CLIENT role. A resolver that only
	// walks realm roles silently under-reports pos-app access — and the output
	// still looks complete.
	snap := retailRealm().
		user("m.huber", nil, model.RealmRole("store-admin")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	g := grantFor(t, res, "m.huber", "client:pos-app:till-operator")
	if !g.Entitlement.IsClientRole() || g.Entitlement.Client != "pos-app" {
		t.Errorf("entitlement = %+v, want a pos-app client role", g.Entitlement)
	}
	if g.Path.Kind() != resolve.PathComposite {
		t.Errorf("path kind = %q, want %q", g.Path.Kind(), resolve.PathComposite)
	}
}

func TestCase2_ClientCompositeContainingClientRole(t *testing.T) {
	snap := retailRealm().
		user("t.till", nil, model.ClientRole("pos-app", "till-admin")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	got := entitlements(t, res, "t.till")
	want := []string{"client:pos-app:till-admin", "client:pos-app:till-operator"}
	if !slices.Equal(got, want) {
		t.Errorf("entitlements = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Correctness case #3 — direct-only output is WRONG, not merely incomplete
// ---------------------------------------------------------------------------

func TestCase3_DirectOnlyExportIsWrong(t *testing.T) {
	snap := retailRealm().
		user("a.gruber", []string{"/Retail/StoreManagers"}).
		user("r.chief", nil, model.RealmRole("regional-manager")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	// a.gruber has NO direct assignments: a direct-mappings export reports her as
	// having no access whatsoever, while she actually holds four entitlements.
	if naive := naiveDirectOnly(snap, "a.gruber"); len(naive) != 0 {
		t.Fatalf("fixture changed: a.gruber should have no direct roles, got %v", naive)
	}
	if got := entitlements(t, res, "a.gruber"); len(got) != 4 {
		t.Errorf("a.gruber resolves to %d entitlements %v, want 4", len(got), got)
	}

	// r.chief has one direct role but five effective ones.
	naive := naiveDirectOnly(snap, "r.chief")
	full := entitlements(t, res, "r.chief")
	if len(naive) != 1 {
		t.Fatalf("fixture changed: r.chief should have 1 direct role, got %v", naive)
	}
	if len(full) != 5 {
		t.Errorf("r.chief resolves to %d entitlements %v, want 5", len(full), full)
	}
	if slices.Equal(naive, full) {
		t.Error("direct-only and resolved output agree; the fixture no longer demonstrates the defect")
	}
}

// ---------------------------------------------------------------------------
// Cycles, diamonds, determinism
// ---------------------------------------------------------------------------

func TestCycleTerminatesAndWarns(t *testing.T) {
	// A -> B -> A. Keycloak may not permit this, but kcac must never hang or
	// panic on data it did not create, and must say so rather than silently
	// truncating.
	snap := newSnapshot().
		composite(model.RealmRole("a"), model.RealmRole("b")).
		composite(model.RealmRole("b"), model.RealmRole("a")).
		user("c.cycle", nil, model.RealmRole("a")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	got := entitlements(t, res, "c.cycle")
	want := []string{"realm:a", "realm:b"}
	if !slices.Equal(got, want) {
		t.Errorf("entitlements = %v, want %v (each role once, no repetition)", got, want)
	}

	var warned bool
	for _, w := range res.Warnings {
		if strings.Contains(strings.ToLower(w), "cycle") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no cycle warning recorded; warnings = %v", res.Warnings)
	}
}

func TestSelfReferencingCompositeIsSafe(t *testing.T) {
	snap := newSnapshot().
		composite(model.RealmRole("a"), model.RealmRole("a")).
		user("s.self", nil, model.RealmRole("a")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})
	if got := entitlements(t, res, "s.self"); !slices.Equal(got, []string{"realm:a"}) {
		t.Errorf("entitlements = %v, want [realm:a]", got)
	}
}

func TestDeepCompositeChainDoesNotOverflow(t *testing.T) {
	// A pathological but legal chain: 500 composites deep.
	b := newSnapshot()
	const depth = 500
	for i := range depth {
		b.composite(model.RealmRole(chainName(i)), model.RealmRole(chainName(i+1)))
	}
	snap := b.user("d.deep", nil, model.RealmRole(chainName(0))).build()

	res := resolve.Resolve(snap, resolve.Options{})
	if got := len(entitlements(t, res, "d.deep")); got != depth+1 {
		t.Errorf("resolved %d entitlements, want %d", got, depth+1)
	}
}

func chainName(i int) string { return "r" + string(rune('A'+i%26)) + strconv.Itoa(i) }

func TestDiamondCountsEveryPathAndPicksShortest(t *testing.T) {
	// d.dual holds store-admin twice: directly, and via /Retail/StoreManagers.
	snap := retailRealm().
		user("d.dual", []string{"/Retail/StoreManagers"}, model.RealmRole("store-admin")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	g := grantFor(t, res, "d.dual", "realm:store-admin")
	if g.PathCount != 2 {
		t.Errorf("PathCount = %d, want 2 (direct and via group)", g.PathCount)
	}
	if len(g.AllPaths) != 2 {
		t.Errorf("AllPaths has %d entries, want 2", len(g.AllPaths))
	}
	// The direct assignment is the simplest true explanation, so it wins.
	if g.Path.Kind() != resolve.PathDirect {
		t.Errorf("chosen path kind = %q, want %q (direct is the simplest explanation)", g.Path.Kind(), resolve.PathDirect)
	}
}

func TestTwoCompositeRoutesToSameLeafPicksShorter(t *testing.T) {
	// long: a -> b -> c -> target. short: a -> target.
	snap := newSnapshot().
		composite(model.RealmRole("a"), model.RealmRole("b"), model.RealmRole("target")).
		composite(model.RealmRole("b"), model.RealmRole("c")).
		composite(model.RealmRole("c"), model.RealmRole("target")).
		user("p.paths", nil, model.RealmRole("a")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	g := grantFor(t, res, "p.paths", "realm:target")
	if len(g.Path.Chain) != 2 {
		names := make([]string, 0, len(g.Path.Chain))
		for _, k := range g.Path.Chain {
			names = append(names, k.Name)
		}
		t.Errorf("chosen chain = %v (len %d), want the 2-hop route a -> target", names, len(g.Path.Chain))
	}
}

func TestResolveIsDeterministic(t *testing.T) {
	// Map iteration order must not leak into output: a CSV that reshuffles
	// between runs cannot be diffed between review cycles.
	build := func() *kc.Snapshot {
		return retailRealm().
			user("a.gruber", []string{"/Retail/StoreManagers"}).
			user("s.novak", []string{"/Retail/StoreManagers/Region-East"}).
			user("d.dual", []string{"/Retail/StoreManagers"}, model.RealmRole("store-admin")).
			build()
	}

	first := resolve.Resolve(build(), resolve.Options{})
	for range 8 {
		next := resolve.Resolve(build(), resolve.Options{})
		if len(next.Grants) != len(first.Grants) {
			t.Fatalf("grant count varies between runs: %d then %d", len(first.Grants), len(next.Grants))
		}
		for i := range first.Grants {
			a, b := first.Grants[i], next.Grants[i]
			if a.Username != b.Username || a.Entitlement != b.Entitlement || a.Path.String() != b.Path.String() {
				t.Fatalf("grant %d differs between runs:\n  %s %s %s\n  %s %s %s",
					i, a.Username, a.Entitlement, a.Path, b.Username, b.Entitlement, b.Path)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Edge cases that must not vanish or crash
// ---------------------------------------------------------------------------

func TestUserWithNoAccessIsReportedNotDropped(t *testing.T) {
	// A user with no entitlements is a review finding, not an absence of data.
	// Dropping them hides an account nobody has judged.
	snap := retailRealm().
		user("n.nobody", nil).
		user("a.gruber", []string{"/Retail/StoreManagers"}).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	if got := entitlements(t, res, "n.nobody"); len(got) != 0 {
		t.Errorf("n.nobody has entitlements %v, want none", got)
	}
	var found bool
	for _, u := range res.UsersWithoutGrants {
		if u.Username == "n.nobody" {
			found = true
		}
	}
	if !found {
		t.Errorf("n.nobody missing from UsersWithoutGrants; got %v", res.UsersWithoutGrants)
	}
}

func TestDisabledUserStillResolved(t *testing.T) {
	// Disabled accounts hold standing access and must remain reviewable.
	snap := retailRealm().
		user("x.disabled", []string{"/Retail/StoreManagers"}).disabled().
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	got := entitlements(t, res, "x.disabled")
	if len(got) == 0 {
		t.Fatal("disabled user resolved to no entitlements; their access is still standing access")
	}
	g := grantFor(t, res, "x.disabled", "realm:store-admin")
	if g.Enabled {
		t.Error("grant should record the account as disabled so a reviewer can see it")
	}
}

func TestEmptyRealm(t *testing.T) {
	res := resolve.Resolve(newSnapshot().build(), resolve.Options{})

	if len(res.Grants) != 0 || len(res.UsersWithoutGrants) != 0 {
		t.Errorf("empty realm produced %d grants and %d users", len(res.Grants), len(res.UsersWithoutGrants))
	}
	if len(res.Warnings) != 0 {
		t.Errorf("empty realm produced warnings: %v", res.Warnings)
	}
}

func TestNilSnapshotDoesNotPanic(t *testing.T) {
	res := resolve.Resolve(nil, resolve.Options{})
	if res == nil {
		t.Fatal("Resolve(nil) returned nil; callers should get an empty result, not a panic")
	}
	if len(res.Grants) != 0 {
		t.Errorf("Resolve(nil) produced %d grants", len(res.Grants))
	}
}

func TestGroupWithNoRolesAndUserWithNoGroups(t *testing.T) {
	snap := newSnapshot().
		group("/Empty").
		user("e.empty", []string{"/Empty"}).
		build()

	res := resolve.Resolve(snap, resolve.Options{})
	if len(res.Grants) != 0 {
		t.Errorf("expected no grants, got %d", len(res.Grants))
	}
}

func TestMissingGroupReferenceIsWarnedNotFatal(t *testing.T) {
	// A membership pointing at a group absent from the tree means the snapshot is
	// inconsistent — usually a concurrent change mid-collection. Warn, do not
	// silently produce a grant with an empty path.
	snap := retailRealm().user("a.gruber", []string{"/Retail/StoreManagers"}).build()
	snap.UserGroupIDs["u1"] = append(snap.UserGroupIDs["u1"], "ghost-group")

	res := resolve.Resolve(snap, resolve.Options{})

	var warned bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "ghost-group") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("unknown group reference not warned about; warnings = %v", res.Warnings)
	}
}

// ---------------------------------------------------------------------------
// Options and rendering
// ---------------------------------------------------------------------------

func TestExcludeDefaultRoles(t *testing.T) {
	// default-roles-<realm> is assigned to every user and expands to account
	// noise. Including it by default is honest; suppressing it must be explicit.
	snap := retailRealm().
		user("a.any", nil, model.RealmRole("default-roles-test"), model.RealmRole("store-admin")).
		build()
	snap.Composites[model.RealmRole("default-roles-test")] = []model.RoleKey{
		model.RealmRole("offline_access"),
		model.ClientRole("account", "view-profile"),
	}

	withDefaults := resolve.Resolve(snap, resolve.Options{})
	if !slices.Contains(entitlements(t, withDefaults, "a.any"), "realm:offline_access") {
		t.Error("default roles should be included unless explicitly excluded")
	}

	without := resolve.Resolve(snap, resolve.Options{ExcludeDefaultRoles: true})
	got := entitlements(t, without, "a.any")
	for _, unwanted := range []string{"realm:default-roles-test", "realm:offline_access", "client:account:view-profile"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("entitlements %v should not contain %s when defaults are excluded", got, unwanted)
		}
	}
	// Real access must survive the filter.
	if !slices.Contains(got, "realm:store-admin") {
		t.Errorf("entitlements %v lost store-admin; the filter is too broad", got)
	}
}

func TestPathRendering(t *testing.T) {
	tests := []struct {
		name string
		path resolve.Path
		want string
		kind resolve.PathKind
	}{
		{
			name: "direct assignment",
			path: resolve.Path{Chain: []model.RoleKey{model.RealmRole("store-admin")}},
			want: "direct assignment",
			kind: resolve.PathDirect,
		},
		{
			name: "direct with composite expansion",
			path: resolve.Path{Chain: []model.RoleKey{
				model.RealmRole("regional-manager"), model.RealmRole("store-admin"),
			}},
			want: "direct assignment of regional-manager → composite store-admin",
			kind: resolve.PathComposite,
		},
		{
			name: "role granted on the group the user belongs to",
			path: resolve.Path{
				MemberGroup: "/Retail/StoreManagers", GrantGroup: "/Retail/StoreManagers",
				Chain: []model.RoleKey{model.RealmRole("store-admin")},
			},
			want: "via group /Retail/StoreManagers",
			kind: resolve.PathGroup,
		},
		{
			name: "role inherited from an ancestor group",
			path: resolve.Path{
				MemberGroup: "/Retail/StoreManagers", GrantGroup: "/Retail",
				Chain: []model.RoleKey{model.RealmRole("base-employee")},
			},
			want: "via group /Retail/StoreManagers (inherited from /Retail)",
			kind: resolve.PathGroup,
		},
		{
			name: "group plus composite",
			path: resolve.Path{
				MemberGroup: "/HQ", GrantGroup: "/HQ",
				Chain: []model.RoleKey{model.RealmRole("regional-manager"), model.RealmRole("store-admin")},
			},
			want: "via group /HQ → composite regional-manager → store-admin",
			kind: resolve.PathGroupComposite,
		},
		{
			name: "client role in the chain is rendered legibly",
			path: resolve.Path{
				Chain: []model.RoleKey{model.RealmRole("store-admin"), model.ClientRole("pos-app", "till-operator")},
			},
			want: "direct assignment of store-admin → composite pos-app:till-operator",
			kind: resolve.PathComposite,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.path.String(); got != tt.want {
				t.Errorf("String() = %q\nwant       %q", got, tt.want)
			}
			if got := tt.path.Kind(); got != tt.kind {
				t.Errorf("Kind() = %q, want %q", got, tt.kind)
			}
		})
	}
}

func TestGrantPathNeverContainsAUUID(t *testing.T) {
	// Nobody can certify f47ac10b-58cc-…; a path that leaks an id is unusable.
	snap := retailRealm().
		user("s.novak", []string{"/Retail/StoreManagers/Region-East"}).
		build()

	res := resolve.Resolve(snap, resolve.Options{})
	for _, g := range res.Grants {
		for _, ref := range []string{g.Path.String(), g.Entitlement.Name} {
			if strings.Contains(ref, "g1") || strings.Contains(ref, "u1") {
				t.Errorf("reviewer-facing text %q leaks an internal id", ref)
			}
		}
	}
}

func TestResultStatsAndOrdering(t *testing.T) {
	snap := retailRealm().
		user("z.last", nil, model.RealmRole("base-employee")).
		user("a.first", nil, model.RealmRole("base-employee")).
		build()

	res := resolve.Resolve(snap, resolve.Options{})

	if res.Realm != "test" {
		t.Errorf("Realm = %q, want test", res.Realm)
	}
	if res.ResolvedAt.IsZero() {
		t.Error("ResolvedAt not set")
	}
	// Sorted by username so two runs diff cleanly.
	if res.Grants[0].Username != "a.first" {
		t.Errorf("first grant is for %q, want a.first (output must be sorted)", res.Grants[0].Username)
	}
}

func TestAncestorDepthCapIsReported(t *testing.T) {
	// Hitting the cap drops the remaining ancestors' grants. Under-reporting
	// access without saying so is the one thing this tool exists not to do, and
	// the loop guard beside this one already warns.
	b := newSnapshot()
	path := ""
	const depth = 260 // above maxAncestorDepth
	for i := range depth {
		path += "/g" + strconv.Itoa(i)
		b.group(path, model.RealmRole("r"+strconv.Itoa(i)))
	}
	snap := b.user("d.deep", []string{path}).build()

	res := resolve.Resolve(snap, resolve.Options{})

	var warned bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "ancestors") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("ancestor walk stopped at the cap without saying so; warnings = %v", res.Warnings)
	}
}

func TestStatsReadsCorrectly(t *testing.T) {
	snap := retailRealm().
		user("a.gruber", []string{"/Retail/StoreManagers"}).
		user("n.nobody", nil).
		build()

	got := resolve.Resolve(snap, resolve.Options{}).Stats()
	want := "4 grants across 2 users (1 with no access)"
	if got != want {
		t.Errorf("Stats() = %q, want %q", got, want)
	}
}
