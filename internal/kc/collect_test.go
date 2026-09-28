package kc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/softika/kcac/internal/model"
)

// fakeRealm serves a complete, small realm so Collect can be driven end to end
// without Docker.
//
// Collect fans out over users, groups and composite roles with an errgroup, and
// every result is folded into shared maps under a mutex. That is the most
// concurrency-heavy code in the repo, and until this test it was only covered by
// the build-tagged integration suite, which does not run by default.
func fakeRealm(t *testing.T) (*fakeKeycloak, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32

	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		requests.Add(1)

		if r.URL.Path == "/admin/serverinfo" {
			write(w, map[string]any{"systemInfo": map[string]string{"version": "26.0.8"}})
			return
		}

		p := strings.TrimPrefix(r.URL.Path, "/admin/realms/test")
		segs := strings.Split(strings.Trim(p, "/"), "/")
		last := segs[len(segs)-1]

		switch {
		case p == "/users/count":
			write(w, 3)

		case p == "/users":
			write(w, []map[string]any{
				{"id": "u1", "username": "a.gruber", "email": "a@x.test", "enabled": true},
				{"id": "u2", "username": "m.huber", "email": "m@x.test", "enabled": true},
				{"id": "u3", "username": "service-account-app", "enabled": true, "serviceAccountClientLink": "c1"},
			})

		case segs[0] == "users" && last == "groups":
			if segs[1] == "u1" {
				write(w, []map[string]any{{"id": "g2", "name": "Managers", "path": "/Retail/Managers"}})
				return
			}
			write(w, []any{})

		case segs[0] == "users" && last == "role-mappings":
			if segs[1] == "u2" {
				write(w, map[string]any{
					"realmMappings": []map[string]any{{"id": "r2", "name": "store-admin", "composite": true}},
				})
				return
			}
			write(w, map[string]any{})

		case p == "/clients":
			write(w, []map[string]any{{"id": "c1", "clientId": "pos-app", "enabled": true}})

		case p == "/roles":
			write(w, []map[string]any{
				{"id": "r1", "name": "base-employee"},
				{"id": "r2", "name": "store-admin", "composite": true},
			})

		case segs[0] == "roles" && last == "composites":
			// store-admin contains a realm role AND a client role.
			write(w, []map[string]any{
				{"id": "r1", "name": "base-employee"},
				{"id": "cr1", "name": "till-operator", "clientRole": true, "containerId": "c1"},
			})

		case segs[0] == "clients" && last == "roles":
			write(w, []map[string]any{{"id": "cr1", "name": "till-operator", "clientRole": true, "containerId": "c1"}})

		case segs[0] == "clients" && last == "composites":
			write(w, []any{})

		case p == "/groups":
			write(w, []map[string]any{{"id": "g1", "name": "Retail", "path": "/Retail"}})

		case last == "children":
			if segs[1] == "g1" {
				write(w, []map[string]any{{"id": "g2", "name": "Managers", "path": "/Retail/Managers"}})
				return
			}
			write(w, []any{})

		case segs[0] == "groups" && last == "role-mappings":
			if segs[1] == "g1" {
				write(w, map[string]any{
					"realmMappings": []map[string]any{{"id": "r1", "name": "base-employee"}},
				})
				return
			}
			write(w, map[string]any{
				"clientMappings": map[string]any{
					"pos-app": map[string]any{
						"id": "c1", "client": "pos-app",
						"mappings": []map[string]any{{"id": "cr1", "name": "till-operator", "clientRole": true, "containerId": "c1"}},
					},
				},
			})

		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return fk, &requests
}

func TestCollectBuildsACompleteSnapshot(t *testing.T) {
	fk, _ := fakeRealm(t)
	c, _ := fk.client(t)

	snap, err := Collect(context.Background(), c, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	if snap.Realm != "test" {
		t.Errorf("Realm = %q", snap.Realm)
	}
	if snap.ServerVersion != "26.0.8" {
		t.Errorf("ServerVersion = %q, want 26.0.8", snap.ServerVersion)
	}
	if snap.ChildrenEndpoint != "supported" {
		t.Errorf("ChildrenEndpoint = %q, want supported", snap.ChildrenEndpoint)
	}
	if snap.CollectedAt.IsZero() {
		t.Error("CollectedAt not set")
	}

	if len(snap.Users) != 3 || len(snap.Groups) != 2 || len(snap.Clients) != 1 {
		t.Fatalf("users/groups/clients = %d/%d/%d, want 3/2/1",
			len(snap.Users), len(snap.Groups), len(snap.Clients))
	}

	// The group tree must be wired up, since ancestor inheritance depends on it.
	var child model.GroupRepresentation
	for _, g := range snap.Groups {
		if g.Path == "/Retail/Managers" {
			child = g
		}
	}
	if child.ParentID != "g1" {
		t.Errorf("/Retail/Managers parent = %q, want g1", child.ParentID)
	}

	// Direct user data, fanned out concurrently.
	if got := snap.UserGroupIDs["u1"]; !slices.Equal(got, []string{"g2"}) {
		t.Errorf("u1 groups = %v, want [g2]", got)
	}
	if got := snap.UserRoles["u2"]; !slices.Equal(got, []model.RoleKey{model.RealmRole("store-admin")}) {
		t.Errorf("u2 roles = %v", got)
	}

	// Group grants, also fanned out.
	if got := snap.GroupRoles["g1"]; !slices.Equal(got, []model.RoleKey{model.RealmRole("base-employee")}) {
		t.Errorf("g1 roles = %v", got)
	}
	if got := snap.GroupRoles["g2"]; !slices.Equal(got, []model.RoleKey{model.ClientRole("pos-app", "till-operator")}) {
		t.Errorf("g2 roles = %v", got)
	}

	// The composite edge crossing into a client role is the one that matters
	// most, and it needs the client UUID translated to a legible clientId.
	edges := snap.Composites[model.RealmRole("store-admin")]
	if !slices.Contains(edges, model.ClientRole("pos-app", "till-operator")) {
		t.Errorf("store-admin composites = %v, want a pos-app client role", edges)
	}
	if !slices.Contains(edges, model.RealmRole("base-employee")) {
		t.Errorf("store-admin composites = %v, want base-employee", edges)
	}
}

func TestCollectIsStableUnderConcurrency(t *testing.T) {
	// Every shared map in Collect is written from an errgroup worker. Repeating
	// the collection at high concurrency under -race is what catches a missing
	// lock or a lost write.
	fk, _ := fakeRealm(t)
	c, _ := fk.client(t, func(cfg *Config) { cfg.Concurrency = 16 })

	first, err := Collect(context.Background(), c, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	for i := range 6 {
		got, err := Collect(context.Background(), c, nil)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if len(got.Users) != len(first.Users) ||
			len(got.Groups) != len(first.Groups) ||
			len(got.Composites) != len(first.Composites) ||
			len(got.GroupRoles) != len(first.GroupRoles) ||
			len(got.UserRoles) != len(first.UserRoles) {
			t.Fatalf("run %d produced a different snapshot: users=%d groups=%d composites=%d groupRoles=%d userRoles=%d",
				i, len(got.Users), len(got.Groups), len(got.Composites), len(got.GroupRoles), len(got.UserRoles))
		}
	}
}

func TestCollectReportsProgress(t *testing.T) {
	fk, _ := fakeRealm(t)
	c, _ := fk.client(t)

	var lines []string
	_, err := Collect(context.Background(), c, func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	joined := strings.Join(lines, "\n")
	for _, want := range []string{"3 users", "clients:", "groups:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress output missing %q:\n%s", want, joined)
		}
	}
}

func TestCollectFailsLoudlyOnAServerError(t *testing.T) {
	// A partial snapshot presented as complete is the worst outcome, so any
	// failure in the fan-out has to abort the collection.
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		if strings.HasSuffix(r.URL.Path, "/users/count") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("3"))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})
	c, _ := fk.client(t)

	if _, err := Collect(context.Background(), c, nil); err == nil {
		t.Fatal("expected Collect to fail when the server refuses a request")
	} else if !IsForbidden(err) {
		t.Errorf("error = %v, want a 403", err)
	}
}

func TestCollectStopsOnContextCancellation(t *testing.T) {
	fk, _ := fakeRealm(t)
	c, _ := fk.client(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Collect(ctx, c, nil); err == nil {
		t.Fatal("expected Collect to stop on a cancelled context")
	}
}
