//go:build integration

// The oracle test: kcac's resolution checked against Keycloak's own.
//
//	docker compose -f test/docker-compose.yml up -d && ./test/wait-for-keycloak.sh
//	go test -tags=integration ./internal/resolve/
//
// Keycloak exposes its effective-roles computation at
// .../role-mappings/realm/composite and .../role-mappings/clients/{id}/composite.
// Those endpoints return a flat set with no provenance, so they cannot produce
// kcac's output — but they are ground truth for WHICH entitlements a user holds,
// which makes them a far stronger check than hand-written expectations. A
// mismatch is either a bug in kcac or a version quirk worth documenting.
//
// Verified empirically on 26.0.8 and 22.0.5: these endpoints DO include roles
// derived from group membership, including from ancestor groups. a.gruber holds
// no direct mappings at all, yet the server reports base-employee (granted on the
// parent group /Retail), store-admin and till-supervisor.
package resolve_test

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/model"
	"github.com/softika/kcac/internal/resolve"
)

func liveClient(t *testing.T) *kc.Client {
	t.Helper()

	url := os.Getenv("KCAC_TEST_URL")
	if url == "" {
		t.Skip("KCAC_TEST_URL not set; start test/docker-compose.yml to run the oracle test")
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

// computedFor returns the entitlement names kcac resolved for one user, split
// into realm roles and roles on the named client.
func computedFor(res *resolve.Result, userID, clientID string) (realmRoles, clientRoles []string) {
	for _, g := range res.Grants {
		if g.UserID != userID {
			continue
		}
		switch {
		case !g.Entitlement.IsClientRole():
			realmRoles = append(realmRoles, g.Entitlement.Name)
		case g.Entitlement.Client == clientID:
			clientRoles = append(clientRoles, g.Entitlement.Name)
		}
	}
	sort.Strings(realmRoles)
	sort.Strings(clientRoles)
	return realmRoles, clientRoles
}

func names(roles []model.RoleRepresentation) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}

func diff(got, want []string) (missing, extra []string) {
	inWant := make(map[string]bool, len(want))
	for _, w := range want {
		inWant[w] = true
	}
	inGot := make(map[string]bool, len(got))
	for _, g := range got {
		inGot[g] = true
	}
	for _, w := range want {
		if !inGot[w] {
			missing = append(missing, w)
		}
	}
	for _, g := range got {
		if !inWant[g] {
			extra = append(extra, g)
		}
	}
	return missing, extra
}

func TestOracleResolutionMatchesKeycloak(t *testing.T) {
	client := liveClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	snap, err := kc.Collect(ctx, client, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	res := resolve.Resolve(snap, resolve.Options{})
	t.Logf("server %s: %s", snap.ServerVersion, res.Stats())

	// pos-app is where the realm/client composite boundary is crossed:
	// store-admin is a REALM role containing the CLIENT role till-operator.
	const clientID = "pos-app"
	var clientUUID string
	for _, c := range snap.Clients {
		if c.ClientID == clientID {
			clientUUID = c.ID
		}
	}
	if clientUUID == "" {
		t.Fatalf("client %s not found in the fixture realm", clientID)
	}

	var checked int
	for _, u := range snap.Users {
		wantRealm, err := client.EffectiveRealmRoles(ctx, u.ID)
		if err != nil {
			t.Fatalf("EffectiveRealmRoles(%s): %v", u.Username, err)
		}
		wantClient, err := client.EffectiveClientRoles(ctx, u.ID, clientUUID)
		if err != nil {
			t.Fatalf("EffectiveClientRoles(%s): %v", u.Username, err)
		}

		gotRealm, gotClient := computedFor(res, u.ID, clientID)

		if missing, extra := diff(gotRealm, names(wantRealm)); len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s: realm roles disagree with Keycloak\n  kcac missed  : %v\n  kcac invented: %v\n  kcac         : %v\n  keycloak     : %v",
				u.Username, missing, extra, gotRealm, names(wantRealm))
		}
		if missing, extra := diff(gotClient, names(wantClient)); len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s: %s client roles disagree with Keycloak\n  kcac missed  : %v\n  kcac invented: %v\n  kcac         : %v\n  keycloak     : %v",
				u.Username, clientID, missing, extra, gotClient, names(wantClient))
		}
		checked++
	}

	if checked < 120 {
		t.Errorf("only %d users checked; the fixture should have at least 120", checked)
	}
	t.Logf("oracle agreement on realm and %s client roles for all %d users", clientID, checked)
}

func TestOracleGroupOnlyUserProvesAncestorInheritance(t *testing.T) {
	// The single most important assertion in the project, checked against the
	// server rather than against my own expectations.
	//
	// a.gruber belongs only to /Retail/StoreManagers and has no direct role
	// mappings whatsoever. Keycloak nonetheless reports base-employee, which is
	// granted on the PARENT group /Retail. Any resolver that reads only the
	// member group — or only direct mappings — disagrees with the server here.
	client := liveClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	snap, err := kc.Collect(ctx, client, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	res := resolve.Resolve(snap, resolve.Options{})

	var gruber model.UserRepresentation
	for _, u := range snap.Users {
		if u.Username == "a.gruber" {
			gruber = u
		}
	}
	if gruber.ID == "" {
		t.Fatal("fixture user a.gruber not found")
	}

	// Precondition: no direct role assignments at all.
	if direct := snap.UserRoles[gruber.ID]; len(direct) != 0 {
		t.Fatalf("a.gruber has direct roles %v; the fixture no longer demonstrates the case", direct)
	}

	wantRealm, err := client.EffectiveRealmRoles(ctx, gruber.ID)
	if err != nil {
		t.Fatalf("EffectiveRealmRoles: %v", err)
	}
	gotRealm, _ := computedFor(res, gruber.ID, "pos-app")

	if missing, extra := diff(gotRealm, names(wantRealm)); len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("a.gruber: missed %v, invented %v (kcac %v vs keycloak %v)",
			missing, extra, gotRealm, names(wantRealm))
	}

	// And the inherited grant must explain itself.
	for _, g := range res.Grants {
		if g.UserID == gruber.ID && g.Entitlement == model.RealmRole("base-employee") {
			if g.Path.GrantGroup != "/Retail" {
				t.Errorf("base-employee GrantGroup = %q, want /Retail", g.Path.GrantGroup)
			}
			if g.Path.MemberGroup != "/Retail/StoreManagers" {
				t.Errorf("base-employee MemberGroup = %q, want /Retail/StoreManagers", g.Path.MemberGroup)
			}
			t.Logf("grant_path: %s", g.Path)
			return
		}
	}
	t.Error("no base-employee grant resolved for a.gruber")
}
