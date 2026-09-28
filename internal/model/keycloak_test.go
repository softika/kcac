package model

import (
	"encoding/json"
	"sort"
	"testing"
)

// Fixture mirrors a real GET /users/{id}/role-mappings body, including the
// clientMappings map keyed by clientId.
const roleMappingsJSON = `{
  "realmMappings": [
    {"id":"r1","name":"default-roles-test","composite":true,"clientRole":false,"containerId":"test"},
    {"id":"r2","name":"store-admin","composite":true,"clientRole":false,"containerId":"test"}
  ],
  "clientMappings": {
    "pos-app": {
      "id":"c-uuid-1","client":"pos-app",
      "mappings":[{"id":"r3","name":"till-operator","composite":false,"clientRole":true,"containerId":"c-uuid-1"}]
    },
    "crm-app": {
      "id":"c-uuid-2","client":"crm-app",
      "mappings":[{"id":"r4","name":"viewer","composite":false,"clientRole":true,"containerId":"c-uuid-2"}]
    }
  }
}`

func TestMappingsRepresentationKeys(t *testing.T) {
	var m MappingsRepresentation
	if err := json.Unmarshal([]byte(roleMappingsJSON), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := m.Keys()
	strs := make([]string, 0, len(got))
	for _, k := range got {
		strs = append(strs, k.String())
	}
	sort.Strings(strs)

	want := []string{
		"client:crm-app:viewer",
		"client:pos-app:till-operator",
		"realm:default-roles-test",
		"realm:store-admin",
	}
	if len(strs) != len(want) {
		t.Fatalf("got %d keys %v, want %d %v", len(strs), strs, len(want), want)
	}
	for i := range want {
		if strs[i] != want[i] {
			t.Errorf("key[%d] = %q, want %q", i, strs[i], want[i])
		}
	}
}

func TestMappingsRepresentationKeysEmpty(t *testing.T) {
	var m MappingsRepresentation
	if got := m.Keys(); len(got) != 0 {
		t.Errorf("empty mappings should yield no keys, got %v", got)
	}
}

func TestMappingsKeysFallsBackToNestedClientName(t *testing.T) {
	// Defensive: if a version ever emits an empty map key, fall back to .client
	// rather than producing a RoleKey with an empty Client (which would silently
	// become a realm role).
	m := MappingsRepresentation{
		ClientMappings: map[string]ClientMappings{
			"": {Client: "pos-app", Mappings: []RoleRepresentation{{Name: "till-operator"}}},
		},
	}
	got := m.Keys()
	if len(got) != 1 {
		t.Fatalf("want 1 key, got %v", got)
	}
	if !got[0].IsClientRole() || got[0].Client != "pos-app" {
		t.Errorf("got %v, want client role on pos-app", got[0].String())
	}
}

func TestGroupRepresentationUnmarshalNested(t *testing.T) {
	// Pre-23 shape: the tree arrives nested.
	const nested = `{"id":"g1","name":"Retail","path":"/Retail","subGroups":[
	  {"id":"g2","name":"StoreManagers","path":"/Retail/StoreManagers","parentId":"g1"}]}`
	var g GroupRepresentation
	if err := json.Unmarshal([]byte(nested), &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(g.SubGroups) != 1 || g.SubGroups[0].Path != "/Retail/StoreManagers" {
		t.Fatalf("nested subGroups not parsed: %+v", g)
	}
}
