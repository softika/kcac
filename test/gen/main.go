// Command gen writes testdata/realm-export.json: the seeded Keycloak realm kcac
// is tested against.
//
// The realm is built in code rather than hand-written JSON so each edge case has
// a name and a comment explaining which failure it guards against. Regenerate
// with: go run ./test/gen
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	realmName     = "kcac-test"
	auditClient   = "kcac-audit"
	auditSecret   = "test-secret" // #nosec G101 — fixture only, never a real credential
	bulkUsers     = 120           // > the /users default max of 100, to force pagination
	wideSubgroups = 15            // > the /children default max of 10, to force pagination
)

type realm struct {
	Realm              string   `json:"realm"`
	Enabled            bool     `json:"enabled"`
	SSLRequired        string   `json:"sslRequired"`
	EventsEnabled      bool     `json:"eventsEnabled"`
	AdminEventsEnabled bool     `json:"adminEventsEnabled"`
	Roles              roles    `json:"roles"`
	Groups             []group  `json:"groups"`
	Clients            []client `json:"clients"`
	Users              []user   `json:"users"`
}

type roles struct {
	Realm  []role            `json:"realm"`
	Client map[string][]role `json:"client"`
}

type role struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Composite   bool        `json:"composite,omitempty"`
	Composites  *composites `json:"composites,omitempty"`
}

type composites struct {
	Realm  []string            `json:"realm,omitempty"`
	Client map[string][]string `json:"client,omitempty"`
}

type group struct {
	Name        string              `json:"name"`
	Path        string              `json:"path"`
	RealmRoles  []string            `json:"realmRoles,omitempty"`
	ClientRoles map[string][]string `json:"clientRoles,omitempty"`
	SubGroups   []group             `json:"subGroups,omitempty"`
}

type client struct {
	ClientID               string `json:"clientId"`
	Enabled                bool   `json:"enabled"`
	PublicClient           bool   `json:"publicClient"`
	ServiceAccountsEnabled bool   `json:"serviceAccountsEnabled"`
	StandardFlowEnabled    bool   `json:"standardFlowEnabled"`
	Secret                 string `json:"secret,omitempty"`
}

type user struct {
	Username               string              `json:"username"`
	Enabled                bool                `json:"enabled"`
	Email                  string              `json:"email,omitempty"`
	FirstName              string              `json:"firstName,omitempty"`
	LastName               string              `json:"lastName,omitempty"`
	RealmRoles             []string            `json:"realmRoles,omitempty"`
	ClientRoles            map[string][]string `json:"clientRoles,omitempty"`
	Groups                 []string            `json:"groups,omitempty"`
	ServiceAccountClientID string              `json:"serviceAccountClientId,omitempty"`
}

func build() realm {
	r := realm{
		Realm:       realmName,
		Enabled:     true,
		SSLRequired: "none",
		// Events off by default, matching most real deployments: kcac must report
		// last_login as unknown rather than inventing "never logged in".
		EventsEnabled:      false,
		AdminEventsEnabled: false,
	}

	// ---- Roles -------------------------------------------------------------
	//
	// The chain regional-manager -> store-admin -> till-supervisor -> base-employee
	// is three composite hops deep, and store-admin also contains a CLIENT role.
	// A resolver that stops at one level, or that only walks realm roles, gets
	// every user below wrong.
	r.Roles = roles{
		Realm: []role{
			{Name: "base-employee", Description: "leaf role, reached only through composites"},
			{
				Name:        "till-supervisor",
				Description: "composite containing another composite",
				Composite:   true,
				Composites:  &composites{Realm: []string{"base-employee"}},
			},
			{
				Name:        "store-admin",
				Description: "composite containing a realm composite AND a client role",
				Composite:   true,
				Composites: &composites{
					Realm:  []string{"till-supervisor"},
					Client: map[string][]string{"pos-app": {"till-operator"}},
				},
			},
			{
				Name:        "regional-manager",
				Description: "top of the three-hop composite chain",
				Composite:   true,
				Composites:  &composites{Realm: []string{"store-admin"}},
			},
			{Name: "orphan-role", Description: "exists but is assigned to nobody"},
		},
		Client: map[string][]role{
			"pos-app": {
				{Name: "till-operator", Description: "leaf client role"},
				{
					Name:        "till-admin",
					Description: "client composite containing a client role",
					Composite:   true,
					Composites:  &composites{Client: map[string][]string{"pos-app": {"till-operator"}}},
				},
			},
			"crm-app": {
				{Name: "viewer"},
			},
		},
	}

	// ---- Groups ------------------------------------------------------------
	//
	// Roles at EVERY level of a three-deep tree. A member of Region-East must
	// inherit base-employee from /Retail and store-admin from
	// /Retail/StoreManagers — the ancestor walk that RoleUtils.addGroupRoles
	// performs server-side and that hand-rolled scripts skip.
	wide := group{Name: "Wide", Path: "/Wide"}
	for i := range wideSubgroups {
		wide.SubGroups = append(wide.SubGroups, group{
			Name: fmt.Sprintf("Sub%02d", i),
			Path: fmt.Sprintf("/Wide/Sub%02d", i),
		})
	}

	r.Groups = []group{
		{
			Name: "Retail", Path: "/Retail",
			RealmRoles: []string{"base-employee"},
			SubGroups: []group{
				{
					Name: "StoreManagers", Path: "/Retail/StoreManagers",
					RealmRoles: []string{"store-admin"},
					SubGroups: []group{
						{
							Name: "Region-East", Path: "/Retail/StoreManagers/Region-East",
							ClientRoles: map[string][]string{"pos-app": {"till-admin"}},
						},
						{Name: "Region-West", Path: "/Retail/StoreManagers/Region-West"},
					},
				},
			},
		},
		{Name: "HQ", Path: "/HQ", RealmRoles: []string{"regional-manager"}},
		wide,
	}

	// ---- Clients -----------------------------------------------------------
	r.Clients = []client{
		{ClientID: "pos-app", Enabled: true, PublicClient: true, StandardFlowEnabled: true},
		{ClientID: "crm-app", Enabled: true, PublicClient: true, StandardFlowEnabled: true},
		{
			ClientID:               auditClient,
			Enabled:                true,
			PublicClient:           false,
			ServiceAccountsEnabled: true,
			Secret:                 auditSecret,
		},
	}

	// ---- Users -------------------------------------------------------------
	r.Users = []user{
		{
			// Direct assignment: grant_path must say "direct assignment".
			Username: "m.huber", Enabled: true, Email: "m.huber@example.test",
			RealmRoles: []string{"store-admin"},
		},
		{
			// Member of a child group only. Must still hold base-employee, which
			// is granted on the PARENT group. This is correctness case #1.
			Username: "a.gruber", Enabled: true, Email: "a.gruber@example.test",
			Groups: []string{"/Retail/StoreManagers"},
		},
		{
			// Three levels of inheritance plus composite expansion across the
			// realm/client boundary.
			Username: "s.novak", Enabled: true, Email: "s.novak@example.test",
			Groups: []string{"/Retail/StoreManagers/Region-East"},
		},
		{
			// Holds store-admin twice over: directly and via group. Exercises
			// path_count and shortest-path selection.
			Username: "d.dual", Enabled: true, Email: "d.dual@example.test",
			RealmRoles: []string{"store-admin"},
			Groups:     []string{"/Retail/StoreManagers"},
		},
		{
			// Disabled accounts still hold grants and must still be reviewable.
			Username: "x.disabled", Enabled: false, Email: "x.disabled@example.test",
			RealmRoles: []string{"base-employee"},
		},
		{
			// Top of the composite chain: one direct role, four effective.
			Username: "r.chief", Enabled: true, Email: "r.chief@example.test",
			Groups: []string{"/HQ"},
		},
		{
			// A user with no access at all: must appear as reviewed-and-empty
			// rather than vanish.
			Username: "n.nobody", Enabled: true, Email: "n.nobody@example.test",
		},
		{
			// The service account kcac itself authenticates as, holding exactly
			// the read-only roles the README documents and nothing more.
			Username: "service-account-" + auditClient, Enabled: true,
			ServiceAccountClientID: auditClient,
			ClientRoles: map[string][]string{
				"realm-management": {"view-users", "view-clients", "view-realm", "query-users", "query-groups"},
			},
		},
	}

	// Bulk users push the realm past the /users default page size of 100.
	for i := range bulkUsers {
		r.Users = append(r.Users, user{
			Username:   fmt.Sprintf("bulk%04d", i),
			Enabled:    true,
			Email:      fmt.Sprintf("bulk%04d@example.test", i),
			RealmRoles: []string{"base-employee"},
		})
	}

	return r
}

func main() {
	out := "testdata/realm-export.json"
	data, err := json.MarshalIndent(build(), "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen: marshal: %v\n", err)
		os.Exit(1)
	}
	data = append(data, '\n')
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gen: write %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", out, len(data))
}
