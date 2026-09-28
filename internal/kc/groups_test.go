package kc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// groupFixture is the tree both server shapes must produce identically:
//
//	/Retail
//	  /Retail/StoreManagers
//	    /Retail/StoreManagers/Region-East
//	/HQ
type groupNode struct {
	id, name, path string
	children       []groupNode
}

var groupFixture = []groupNode{
	{id: "g1", name: "Retail", path: "/Retail", children: []groupNode{
		{id: "g2", name: "StoreManagers", path: "/Retail/StoreManagers", children: []groupNode{
			{id: "g3", name: "Region-East", path: "/Retail/StoreManagers/Region-East"},
		}},
	}},
	{id: "g4", name: "HQ", path: "/HQ"},
}

// nested renders a node the way Keycloak before 23 does: the whole subtree
// inline.
func (n groupNode) nested() map[string]any {
	kids := make([]map[string]any, 0, len(n.children))
	for _, c := range n.children {
		kids = append(kids, c.nested())
	}
	return map[string]any{"id": n.id, "name": n.name, "path": n.path, "subGroups": kids}
}

// shallow renders a node the way Keycloak 23+ does: no descendants.
func (n groupNode) shallow() map[string]any {
	return map[string]any{"id": n.id, "name": n.name, "path": n.path, "subGroups": []any{}}
}

func findNode(nodes []groupNode, id string) (groupNode, bool) {
	for _, n := range nodes {
		if n.id == id {
			return n, true
		}
		if found, ok := findNode(n.children, id); ok {
			return found, true
		}
	}
	return groupNode{}, false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// assertFixtureTree checks the flattened output regardless of which server shape
// produced it: same groups, same paths, same parent links.
func assertFixtureTree(t *testing.T, got []gotGroup) {
	t.Helper()
	want := []gotGroup{
		{ID: "g1", Path: "/Retail", ParentID: ""},
		{ID: "g2", Path: "/Retail/StoreManagers", ParentID: "g1"},
		{ID: "g3", Path: "/Retail/StoreManagers/Region-East", ParentID: "g2"},
		{ID: "g4", Path: "/HQ", ParentID: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d groups %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

type gotGroup struct {
	ID       string
	Path     string
	ParentID string
}

func flatten(t *testing.T, c *Client) []gotGroup {
	t.Helper()
	tree, err := c.GroupTree(context.Background())
	if err != nil {
		t.Fatalf("GroupTree() error = %v", err)
	}
	out := make([]gotGroup, 0, len(tree))
	for _, g := range tree {
		if len(g.SubGroups) != 0 {
			t.Errorf("flattened group %s still carries nested SubGroups", g.ID)
		}
		out = append(out, gotGroup{ID: g.ID, Path: g.Path, ParentID: g.ParentID})
	}
	return out
}

func TestGroupTreeModernServerUsesChildrenEndpoint(t *testing.T) {
	var childrenCalls atomic.Int32

	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/groups"):
			// Keycloak 23+: descendants omitted.
			writeJSON(w, []map[string]any{groupFixture[0].shallow(), groupFixture[1].shallow()})

		case strings.HasSuffix(r.URL.Path, "/children"):
			childrenCalls.Add(1)
			parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/children"), "/")
			id := parts[len(parts)-1]
			node, ok := findNode(groupFixture, id)
			if !ok {
				writeJSON(w, []any{})
				return
			}
			kids := make([]map[string]any, 0, len(node.children))
			for _, c := range node.children {
				kids = append(kids, c.shallow())
			}
			writeJSON(w, kids)

		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	c, _ := fk.client(t)
	assertFixtureTree(t, flatten(t, c))

	if childrenCalls.Load() != 4 {
		t.Errorf("/children called %d times, want 4 (once per group)", childrenCalls.Load())
	}
	known, supported := c.SupportsGroupChildren()
	if !known || !supported {
		t.Errorf("SupportsGroupChildren() = (%v,%v), want (true,true)", known, supported)
	}
}

func TestGroupTreeLegacyServerFallsBackToNestedSubGroups(t *testing.T) {
	// A pre-23 server can signal the missing GET two different ways, and both
	// must trigger the fallback. Keycloak 22 really returns 405, not 404: the
	// /children path exists there for POST, so RESTEasy reports a missing method
	// rather than a missing resource. Treating only 404 as absence breaks the
	// fallback on the exact servers that need it.
	absenceCodes := map[string]int{
		"404 Not Found":          http.StatusNotFound,
		"405 Method Not Allowed": http.StatusMethodNotAllowed,
	}

	for name, code := range absenceCodes {
		t.Run(name, func(t *testing.T) {
			var childrenProbes atomic.Int32

			fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/children"):
					childrenProbes.Add(1)
					w.WriteHeader(code)

				case strings.HasSuffix(r.URL.Path, "/groups"):
					writeJSON(w, []map[string]any{groupFixture[0].nested(), groupFixture[1].nested()})

				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})

			c, _ := fk.client(t)
			assertFixtureTree(t, flatten(t, c))

			// Probed once and cached — not re-probed per group.
			if got := childrenProbes.Load(); got != 1 {
				t.Errorf("/children probed %d times, want exactly 1 (capability must be cached)", got)
			}
			known, supported := c.SupportsGroupChildren()
			if !known || supported {
				t.Errorf("SupportsGroupChildren() = (%v,%v), want (true,false)", known, supported)
			}
		})
	}
}

func TestGroupTreePaginatesChildrenPastServerDefaultOfTen(t *testing.T) {
	// The /children server-side default max is 10. A group with 25 children must
	// still yield 25: this is the exact trap that silently truncates a group tree
	// and turns an access review into wrong evidence.
	const childCount = 25

	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/groups"):
			writeJSON(w, []map[string]any{{"id": "parent", "name": "Parent", "path": "/Parent"}})

		case strings.HasSuffix(r.URL.Path, "/children"):
			if !strings.Contains(r.URL.Path, "/parent/") {
				writeJSON(w, []any{})
				return
			}
			first, max := queryInt(r, "first"), queryInt(r, "max")
			if max == 0 {
				t.Error("kcac must always send an explicit max on /children")
				max = 10
			}
			kids := []map[string]any{}
			for i := first; i < min(first+max, childCount); i++ {
				kids = append(kids, map[string]any{
					"id": fmt.Sprintf("c%02d", i), "name": fmt.Sprintf("Child%02d", i),
					"path": fmt.Sprintf("/Parent/Child%02d", i),
				})
			}
			writeJSON(w, kids)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// PageSize 10 mirrors the server default and forces multiple pages.
	c, _ := fk.client(t, func(cfg *Config) { cfg.PageSize = 10 })

	got := flatten(t, c)
	if len(got) != childCount+1 {
		t.Fatalf("got %d groups, want %d (parent + %d children) — children were truncated", len(got), childCount+1, childCount)
	}
	if got[len(got)-1].ID != "c24" {
		t.Errorf("last group = %s, want c24", got[len(got)-1].ID)
	}
}

func TestGroupTreeDerivesPathWhenServerOmitsIt(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/children"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/groups"):
			// No "path" anywhere: kcac must derive it rather than emit empties.
			writeJSON(w, []map[string]any{{
				"id": "g1", "name": "Retail",
				"subGroups": []map[string]any{{"id": "g2", "name": "StoreManagers"}},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	c, _ := fk.client(t)
	got := flatten(t, c)

	want := []gotGroup{
		{ID: "g1", Path: "/Retail", ParentID: ""},
		{ID: "g2", Path: "/Retail/StoreManagers", ParentID: "g1"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGroupTreeIgnoresRepeatedGroupIDs(t *testing.T) {
	// A duplicated id would otherwise duplicate a subtree and inflate every
	// grant derived from it.
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/children"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/groups"):
			dup := groupFixture[0].nested()
			writeJSON(w, []map[string]any{dup, dup})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	c, _ := fk.client(t)
	got := flatten(t, c)
	if len(got) != 3 {
		t.Fatalf("got %d groups %+v, want 3 (duplicate subtree ignored)", len(got), got)
	}
}

func TestGroupTreePropagatesRealErrors(t *testing.T) {
	// A 403 on /children means a missing role, not an old server: it must not be
	// silently swallowed into the nested-subGroups fallback.
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		if strings.HasSuffix(r.URL.Path, "/children") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeJSON(w, []map[string]any{groupFixture[0].shallow()})
	})

	c, _ := fk.client(t)
	if _, err := c.GroupTree(context.Background()); err == nil {
		t.Fatal("expected GroupTree to fail on 403, not fall back")
	} else if !IsForbidden(err) {
		t.Errorf("error = %v, want a 403", err)
	}
}

func queryInt(r *http.Request, key string) int {
	var n int
	_, _ = fmt.Sscanf(r.URL.Query().Get(key), "%d", &n)
	return n
}
