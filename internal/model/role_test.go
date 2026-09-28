package model

import (
	"encoding/json"
	"testing"
)

func TestRoleKey(t *testing.T) {
	tests := []struct {
		name       string
		key        RoleKey
		wantString string
		wantClient bool
		wantType   string
	}{
		{
			name:       "realm role",
			key:        RealmRole("store-admin"),
			wantString: "realm:store-admin",
			wantClient: false,
			wantType:   "realm_role",
		},
		{
			name:       "client role",
			key:        ClientRole("pos-app", "store-admin"),
			wantString: "client:pos-app:store-admin",
			wantClient: true,
			wantType:   "client_role",
		},
		{
			name:       "role name containing a colon stays unambiguous by prefix",
			key:        RealmRole("ns:admin"),
			wantString: "realm:ns:admin",
			wantClient: false,
			wantType:   "realm_role",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.key.String(); got != tt.wantString {
				t.Errorf("String() = %q, want %q", got, tt.wantString)
			}
			if got := tt.key.IsClientRole(); got != tt.wantClient {
				t.Errorf("IsClientRole() = %v, want %v", got, tt.wantClient)
			}
			if got := tt.key.EntitlementType(); got != tt.wantType {
				t.Errorf("EntitlementType() = %q, want %q", got, tt.wantType)
			}
		})
	}
}

func TestRoleKeyIdentityAsMapKey(t *testing.T) {
	// A realm role and a client role of the same name must never collide,
	// because a composite realm role can contain a client role of that name.
	m := map[RoleKey]string{
		RealmRole("admin"):             "realm",
		ClientRole("pos-app", "admin"): "pos-app",
		ClientRole("crm-app", "admin"): "crm-app",
	}
	if len(m) != 3 {
		t.Fatalf("expected 3 distinct keys, got %d: %v", len(m), m)
	}
}

func TestRoleKeyZero(t *testing.T) {
	if !(RoleKey{}).Zero() {
		t.Error("empty RoleKey should report Zero")
	}
	if RealmRole("x").Zero() {
		t.Error("populated RoleKey should not report Zero")
	}
}

func TestRoleKeyTextRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		key  RoleKey
		want string
	}{
		{"realm role", RealmRole("store-admin"), "realm:store-admin"},
		{"client role", ClientRole("pos-app", "till-operator"), "client:pos-app:till-operator"},
		{"realm role with a colon in the name", RealmRole("ns:admin"), "realm:ns:admin"},
		{"client role with a colon in the ROLE name", ClientRole("pos-app", "ns:admin"), "client:pos-app:ns:admin"},
		{"client role with a colon in the CLIENT id", ClientRole("urn:pos", "admin"), "client:urn%3Apos:admin"},
		// The escape character itself must be escaped, or the encoding has no
		// unique inverse and two different clients collapse into one map key.
		{"client id containing a percent sign", ClientRole("a%b", "admin"), "client:a%25b:admin"},
		{"client id containing a literal %3A", ClientRole("a%3Ab", "admin"), "client:a%253Ab:admin"},
		{"client id containing a literal %25", ClientRole("a%25b", "admin"), "client:a%2525b:admin"},
		{"client id with both a colon and a percent", ClientRole("a%b:c", "admin"), "client:a%25b%3Ac:admin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, err := tt.key.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText() error = %v", err)
			}
			if string(text) != tt.want {
				t.Errorf("MarshalText() = %q, want %q", text, tt.want)
			}

			var back RoleKey
			if err := back.UnmarshalText(text); err != nil {
				t.Fatalf("UnmarshalText(%q) error = %v", text, err)
			}
			if back != tt.key {
				t.Errorf("round trip produced %+v, want %+v", back, tt.key)
			}
		})
	}
}

func TestRoleKeyUnmarshalTextRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "store-admin", "group:x", "client:noseparator"} {
		var k RoleKey
		if err := k.UnmarshalText([]byte(in)); err == nil {
			t.Errorf("UnmarshalText(%q) = nil, want an error", in)
		}
	}
}

func TestRoleKeySurvivesJSONMapRoundTrip(t *testing.T) {
	// The composite graph is serialised as a map keyed by RoleKey, which is the
	// reason MarshalText exists at all.
	original := map[RoleKey][]RoleKey{
		RealmRole("store-admin"):       {RealmRole("till-supervisor"), ClientRole("pos-app", "till-operator")},
		ClientRole("pos-app", "admin"): {ClientRole("pos-app", "till-operator")},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var back map[RoleKey][]RoleKey
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", data, err)
	}

	if len(back) != len(original) {
		t.Fatalf("got %d keys, want %d: %s", len(back), len(original), data)
	}
	for key, want := range original {
		got, ok := back[key]
		if !ok {
			t.Errorf("key %s lost in round trip", key)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("key %s: got %v, want %v", key, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("key %s edge %d: got %s, want %s", key, i, got[i], want[i])
			}
		}
	}
}

func TestRoleKeyEncodingIsInjective(t *testing.T) {
	// Two different clients must never produce the same wire form. RoleKey is a
	// map key, so a collision silently merges two roles and discards one role's
	// composite edges — under-reporting entitlements with no error anywhere.
	clients := []string{
		"pos-app", "a:b", "a%3Ab", "a%25b", "a%b", "%", "%3A", "%25",
		"urn:pos", "a%b:c", "", // the empty client is a realm role
	}

	seen := make(map[string]RoleKey, len(clients))
	for _, c := range clients {
		k := RoleKey{Client: c, Name: "admin"}
		wire := k.String()

		if prev, clash := seen[wire]; clash && prev != k {
			t.Errorf("collision: client %q and client %q both encode to %q", prev.Client, c, wire)
		}
		seen[wire] = k

		var back RoleKey
		if err := back.UnmarshalText([]byte(wire)); err != nil {
			t.Errorf("client %q: UnmarshalText(%q) error = %v", c, wire, err)
			continue
		}
		if back != k {
			t.Errorf("client %q: round trip via %q produced client %q", c, wire, back.Client)
		}
	}
}

func TestRoleKeyMapSurvivesAdversarialClientIDs(t *testing.T) {
	// The exact failure: distinct keys in, fewer keys out, one role's edges gone.
	original := map[RoleKey][]RoleKey{
		ClientRole("a:b", "admin"):   {RealmRole("from-colon")},
		ClientRole("a%3Ab", "admin"): {RealmRole("from-percent")},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var back map[RoleKey][]RoleKey
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(back) != len(original) {
		t.Fatalf("%d keys in, %d out — a composite role's edges were discarded: %s",
			len(original), len(back), data)
	}
	for k, want := range original {
		got, ok := back[k]
		if !ok {
			t.Errorf("key %s lost", k)
			continue
		}
		if len(got) != 1 || got[0] != want[0] {
			t.Errorf("key %s: got %v, want %v", k, got, want)
		}
	}
}
