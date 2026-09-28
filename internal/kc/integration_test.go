//go:build integration

// Integration tests against the seeded realm in test/docker-compose.yml.
//
//	docker compose -f test/docker-compose.yml up -d && ./test/wait-for-keycloak.sh
//	go test -tags=integration ./...
//
// Every assertion here is version-agnostic on purpose: the same expectations must
// hold whether the server serves GET /groups/{id}/children (Keycloak 23+) or
// nests the tree in /groups (earlier), which is what proves the two code paths
// agree.
package kc_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/model"
)

func testClient(t *testing.T) *kc.Client {
	t.Helper()

	url := os.Getenv("KCAC_TEST_URL")
	if url == "" {
		t.Skip("KCAC_TEST_URL not set; start test/docker-compose.yml to run integration tests")
	}

	cfg := kc.Config{
		BaseURL:      url,
		Realm:        envOr("KCAC_TEST_REALM", "kcac-test"),
		ClientID:     envOr("KCAC_TEST_CLIENT_ID", "kcac-audit"),
		ClientSecret: envOr("KCAC_CLIENT_SECRET", "test-secret"),
	}
	client, err := kc.New(cfg)
	if err != nil {
		t.Fatalf("kc.New() error = %v", err)
	}
	return client
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func collect(t *testing.T) *kc.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	snap, err := kc.Collect(ctx, testClient(t), nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	return snap
}

func TestIntegrationCapabilityDetected(t *testing.T) {
	snap := collect(t)

	// Whichever shape the server has, detection must reach a conclusion. An
	// "unknown" here means the probe silently failed.
	if snap.ChildrenEndpoint != "supported" && snap.ChildrenEndpoint != "unsupported" {
		t.Errorf("ChildrenEndpoint = %q, want supported or unsupported", snap.ChildrenEndpoint)
	}
	t.Logf("server %s: /groups/{id}/children %s", snap.ServerVersion, snap.ChildrenEndpoint)
}

func TestIntegrationGroupTree(t *testing.T) {
	snap := collect(t)

	paths := make(map[string]model.GroupRepresentation, len(snap.Groups))
	for _, g := range snap.Groups {
		paths[g.Path] = g
	}

	// Three levels deep, with siblings.
	for _, want := range []string{
		"/Retail",
		"/Retail/StoreManagers",
		"/Retail/StoreManagers/Region-East",
		"/Retail/StoreManagers/Region-West",
		"/HQ",
		"/Wide",
	} {
		if _, ok := paths[want]; !ok {
			t.Errorf("group %s missing from the tree", want)
		}
	}

	// Parent links must be wired, since ancestor-role inheritance depends on
	// them entirely.
	east := paths["/Retail/StoreManagers/Region-East"]
	managers := paths["/Retail/StoreManagers"]
	retail := paths["/Retail"]
	if east.ParentID != managers.ID {
		t.Errorf("Region-East parent = %q, want StoreManagers %q", east.ParentID, managers.ID)
	}
	if managers.ParentID != retail.ID {
		t.Errorf("StoreManagers parent = %q, want Retail %q", managers.ParentID, retail.ID)
	}
	if retail.ParentID != "" {
		t.Errorf("Retail is a root but has parent %q", retail.ParentID)
	}

	// /Wide has 15 children, above the /children server-side default of 10. A
	// truncating implementation loses five of them.
	var wide int
	for _, g := range snap.Groups {
		if g.ParentID == paths["/Wide"].ID {
			wide++
		}
	}
	if wide != 15 {
		t.Errorf("/Wide has %d children, want 15 — children were truncated", wide)
	}
}

func TestIntegrationGroupRoleGrantsAtEveryLevel(t *testing.T) {
	snap := collect(t)

	byPath := make(map[string]string, len(snap.Groups)) // path -> id
	for _, g := range snap.Groups {
		byPath[g.Path] = g.ID
	}

	// Roles are granted at all three levels. This is the raw input that makes
	// ancestor inheritance testable downstream.
	want := map[string]model.RoleKey{
		"/Retail":                           model.RealmRole("base-employee"),
		"/Retail/StoreManagers":             model.RealmRole("store-admin"),
		"/Retail/StoreManagers/Region-East": model.ClientRole("pos-app", "till-admin"),
		"/HQ":                               model.RealmRole("regional-manager"),
	}
	for path, role := range want {
		got := snap.GroupRoles[byPath[path]]
		if !slices.Contains(got, role) {
			t.Errorf("group %s grants %v, want it to include %s", path, got, role)
		}
	}
}

func TestIntegrationCompositeGraphCrossesRealmClientBoundary(t *testing.T) {
	snap := collect(t)

	// store-admin contains a realm composite AND a client role. A resolver that
	// only walks realm roles silently under-reports pos-app access.
	storeAdmin := snap.Composites[model.RealmRole("store-admin")]
	for _, want := range []model.RoleKey{
		model.RealmRole("till-supervisor"),
		model.ClientRole("pos-app", "till-operator"),
	} {
		if !slices.Contains(storeAdmin, want) {
			t.Errorf("store-admin contains %v, want it to include %s", storeAdmin, want)
		}
	}

	// The full chain must be present as edges:
	// regional-manager -> store-admin -> till-supervisor -> base-employee
	chain := []struct{ from, to model.RoleKey }{
		{model.RealmRole("regional-manager"), model.RealmRole("store-admin")},
		{model.RealmRole("till-supervisor"), model.RealmRole("base-employee")},
		{model.ClientRole("pos-app", "till-admin"), model.ClientRole("pos-app", "till-operator")},
	}
	for _, edge := range chain {
		if !slices.Contains(snap.Composites[edge.from], edge.to) {
			t.Errorf("composite edge %s -> %s missing", edge.from, edge.to)
		}
	}
}

func TestIntegrationUsersPaginatePastDefault(t *testing.T) {
	snap := collect(t)

	// The fixture seeds 120 bulk users, above the /users default max of 100.
	if len(snap.Users) < 120 {
		t.Errorf("collected %d users, want at least 120 — pagination stopped early", len(snap.Users))
	}

	var seenFirst, seenLast bool
	for _, u := range snap.Users {
		switch u.Username {
		case "bulk0000":
			seenFirst = true
		case "bulk0119":
			seenLast = true
		}
	}
	if !seenFirst || !seenLast {
		t.Errorf("bulk0000 seen=%v, bulk0119 seen=%v — want both, proving every page was read", seenFirst, seenLast)
	}
}

func TestIntegrationGroupOnlyUserHasNoDirectRoles(t *testing.T) {
	snap := collect(t)

	var gruber model.UserRepresentation
	for _, u := range snap.Users {
		if u.Username == "a.gruber" {
			gruber = u
		}
	}
	if gruber.ID == "" {
		t.Fatal("fixture user a.gruber not found")
	}

	// This is the whole argument for kcac in one assertion. a.gruber holds NO
	// direct role assignments — a direct-mappings export shows her with no
	// access. Her real access comes entirely from group membership plus the
	// ancestor walk, and the resolver is what recovers it.
	for _, r := range snap.UserRoles[gruber.ID] {
		if r.Name != "default-roles-"+snap.Realm {
			t.Errorf("a.gruber has direct role %s; the fixture intends group-only access", r)
		}
	}

	groups := snap.UserGroupIDs[gruber.ID]
	if len(groups) != 1 {
		t.Fatalf("a.gruber is in %d groups, want exactly 1", len(groups))
	}
	var path string
	for _, g := range snap.Groups {
		if g.ID == groups[0] {
			path = g.Path
		}
	}
	if path != "/Retail/StoreManagers" {
		t.Errorf("a.gruber is in %q, want /Retail/StoreManagers", path)
	}
}

func TestIntegrationDisabledUserStillCollected(t *testing.T) {
	snap := collect(t)

	for _, u := range snap.Users {
		if u.Username == "x.disabled" {
			if u.Enabled {
				t.Error("x.disabled should be disabled in the fixture")
			}
			// Disabled accounts still hold grants and must remain reviewable:
			// dropping them hides standing access from the reviewer.
			return
		}
	}
	t.Error("disabled fixture user x.disabled was not collected")
}
