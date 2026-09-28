package output_test

import (
	"bytes"
	"encoding/csv"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/softika/kcac/internal/model"
	"github.com/softika/kcac/internal/output"
	"github.com/softika/kcac/internal/resolve"
)

// readCSV parses rendered output into a header and a map-per-row for readable
// assertions.
func readCSV(t *testing.T, data string) ([]string, []map[string]string) {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\n%s", err, data)
	}
	if len(recs) == 0 {
		t.Fatal("no records, not even a header")
	}
	header := recs[0]
	rows := make([]map[string]string, 0, len(recs)-1)
	for _, rec := range recs[1:] {
		m := make(map[string]string, len(header))
		for i, h := range header {
			m[h] = rec[i]
		}
		rows = append(rows, m)
	}
	return header, rows
}

func sampleResult() *resolve.Result {
	return &resolve.Result{
		Realm: "test",
		Grants: []resolve.Grant{
			{
				UserID: "u1", Username: "a.gruber", Email: "a.gruber@example.test", Enabled: true,
				Entitlement: model.RealmRole("base-employee"),
				Path: resolve.Path{
					MemberGroup: "/Retail/StoreManagers", GrantGroup: "/Retail",
					Chain: []model.RoleKey{model.RealmRole("base-employee")},
				},
				PathCount: 1,
			},
			{
				UserID: "u1", Username: "a.gruber", Email: "a.gruber@example.test", Enabled: true,
				Entitlement: model.ClientRole("pos-app", "till-operator"),
				Path: resolve.Path{
					MemberGroup: "/Retail/StoreManagers", GrantGroup: "/Retail/StoreManagers",
					Chain: []model.RoleKey{model.RealmRole("store-admin"), model.ClientRole("pos-app", "till-operator")},
				},
				PathCount: 2,
			},
			{
				UserID: "u2", Username: "x.disabled", Email: "x@example.test", Enabled: false,
				Entitlement: model.RealmRole("base-employee"),
				Path:        resolve.Path{Chain: []model.RoleKey{model.RealmRole("base-employee")}},
				PathCount:   1,
			},
		},
		UsersWithoutGrants: []model.UserRepresentation{
			{ID: "u3", Username: "n.nobody", Email: "n@example.test", Enabled: true},
		},
	}
}

func render(t *testing.T, res *resolve.Result, opts output.Options) string {
	t.Helper()
	var buf bytes.Buffer
	if err := output.WriteCSV(&buf, res, opts); err != nil {
		t.Fatalf("WriteCSV() error = %v", err)
	}
	return buf.String()
}

func TestWriteCSVHeaderIsStable(t *testing.T) {
	header, _ := readCSV(t, render(t, sampleResult(), output.Options{}))

	want := []string{
		"user_id", "username", "email", "enabled",
		"entitlement", "entitlement_type", "client_id",
		"grant_path", "path_kind", "path_count", "account_class", "last_login",
	}
	if !slices.Equal(header, want) {
		t.Errorf("header = %v\nwant       %v", header, want)
	}
}

func TestWriteCSVRendersGrantProvenance(t *testing.T) {
	_, rows := readCSV(t, render(t, sampleResult(), output.Options{}))

	var inherited map[string]string
	for _, r := range rows {
		if r["username"] == "a.gruber" && r["entitlement"] == "base-employee" {
			inherited = r
		}
	}
	if inherited == nil {
		t.Fatal("no row for a.gruber / base-employee")
	}

	// The reason the grant exists is the column that makes the file a review
	// rather than a database dump.
	if want := "via group /Retail/StoreManagers (inherited from /Retail)"; inherited["grant_path"] != want {
		t.Errorf("grant_path = %q, want %q", inherited["grant_path"], want)
	}
	if inherited["path_kind"] != "group" {
		t.Errorf("path_kind = %q, want group", inherited["path_kind"])
	}
	if inherited["entitlement_type"] != "realm_role" {
		t.Errorf("entitlement_type = %q, want realm_role", inherited["entitlement_type"])
	}
	if inherited["client_id"] != "" {
		t.Errorf("client_id = %q, want empty for a realm role", inherited["client_id"])
	}
	if inherited["account_class"] != "human" {
		t.Errorf("account_class = %q, want human", inherited["account_class"])
	}
}

func TestWriteCSVSplitsClientRoleIntoNameAndClient(t *testing.T) {
	_, rows := readCSV(t, render(t, sampleResult(), output.Options{}))

	for _, r := range rows {
		if r["entitlement"] != "till-operator" {
			continue
		}
		if r["client_id"] != "pos-app" {
			t.Errorf("client_id = %q, want pos-app", r["client_id"])
		}
		if r["entitlement_type"] != "client_role" {
			t.Errorf("entitlement_type = %q, want client_role", r["entitlement_type"])
		}
		if r["path_count"] != "2" {
			t.Errorf("path_count = %q, want 2", r["path_count"])
		}
		return
	}
	t.Fatal("no row for the client role till-operator")
}

func TestWriteCSVIncludesAccountsWithNoAccess(t *testing.T) {
	_, rows := readCSV(t, render(t, sampleResult(), output.Options{}))

	for _, r := range rows {
		if r["username"] != "n.nobody" {
			continue
		}
		// An account nobody has judged is a finding; silence looks identical to
		// absence of data.
		if r["entitlement"] != "" || r["entitlement_type"] != "" {
			t.Errorf("no-access row carries an entitlement: %v", r)
		}
		if r["grant_path"] != "(no access)" {
			t.Errorf("grant_path = %q, want (no access)", r["grant_path"])
		}
		if r["path_kind"] != "none" || r["path_count"] != "0" {
			t.Errorf("path_kind = %q, path_count = %q, want none/0", r["path_kind"], r["path_count"])
		}
		return
	}
	t.Fatal("account with no access was dropped from the CSV")
}

func TestWriteCSVOrdersByUsernameThenEntitlement(t *testing.T) {
	_, rows := readCSV(t, render(t, sampleResult(), output.Options{}))

	var got []string
	for _, r := range rows {
		got = append(got, r["username"]+"/"+r["entitlement"])
	}
	want := []string{
		"a.gruber/base-employee",
		"a.gruber/till-operator",
		"n.nobody/",
		"x.disabled/base-employee",
	}
	if !slices.Equal(got, want) {
		t.Errorf("row order = %v\nwant           %v\n(accounts with no access must interleave, not be appended)", got, want)
	}
}

func TestWriteCSVRecordsDisabledAccounts(t *testing.T) {
	_, rows := readCSV(t, render(t, sampleResult(), output.Options{}))

	for _, r := range rows {
		if r["username"] == "x.disabled" {
			if r["enabled"] != "false" {
				t.Errorf("enabled = %q, want false", r["enabled"])
			}
			return
		}
	}
	t.Fatal("disabled account missing; its access is still standing access")
}

func TestLastLoginIsEmptyUnlessKnown(t *testing.T) {
	when := time.Date(2026, 9, 20, 14, 3, 11, 0, time.UTC)

	tests := []struct {
		name  string
		given output.LastLogin
		want  map[string]string // username -> expected cell
	}{
		{
			name:  "not requested",
			given: output.LastLogin{},
			want:  map[string]string{"a.gruber": "", "x.disabled": ""},
		},
		{
			name: "requested but events are disabled on the realm",
			given: output.LastLogin{
				Requested: true, Available: false,
				Reason: "event logging is disabled on this realm",
			},
			want: map[string]string{"a.gruber": "", "x.disabled": ""},
		},
		{
			name: "available, one user seen inside the window",
			given: output.LastLogin{
				Requested: true, Available: true, Window: 90 * 24 * time.Hour,
				ByUserID: map[string]time.Time{"u1": when},
			},
			// u2 had no login inside the window. That is NOT "never": they may
			// have logged in before the window, or before retention expired.
			want: map[string]string{"a.gruber": "2026-09-20T14:03:11Z", "x.disabled": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, rows := readCSV(t, render(t, sampleResult(), output.Options{LastLogin: tt.given}))
			for _, r := range rows {
				want, ok := tt.want[r["username"]]
				if !ok {
					continue
				}
				if r["last_login"] != want {
					t.Errorf("%s last_login = %q, want %q", r["username"], r["last_login"], want)
				}
				if strings.Contains(strings.ToLower(r["last_login"]), "never") {
					t.Errorf("%s: last_login must never claim 'never' from absent events", r["username"])
				}
			}
		})
	}
}

func TestWriteCSVEmptyResult(t *testing.T) {
	header, rows := readCSV(t, render(t, &resolve.Result{Realm: "empty"}, output.Options{}))
	if len(header) != len(output.Header) {
		t.Errorf("header missing from an empty result")
	}
	if len(rows) != 0 {
		t.Errorf("expected no rows, got %d", len(rows))
	}
}

func TestWriteCSVNilResultErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := output.WriteCSV(&buf, nil, output.Options{}); err == nil {
		t.Error("WriteCSV(nil) should error rather than emit a misleading empty file")
	}
}

func TestWriteCSVEscapesSeparatorsInValues(t *testing.T) {
	// Group paths and role names can contain commas and quotes; the file must
	// still parse.
	res := &resolve.Result{
		Realm: "test",
		Grants: []resolve.Grant{{
			UserID: "u1", Username: `o'brien, sean`, Email: "o@example.test", Enabled: true,
			Entitlement: model.RealmRole(`role,with"quotes`),
			Path: resolve.Path{
				MemberGroup: `/Group, With "Comma"`, GrantGroup: `/Group, With "Comma"`,
				Chain: []model.RoleKey{model.RealmRole(`role,with"quotes`)},
			},
			PathCount: 1,
		}},
	}

	_, rows := readCSV(t, render(t, res, output.Options{}))
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0]["username"] != `o'brien, sean` {
		t.Errorf("username = %q, round trip failed", rows[0]["username"])
	}
	if !strings.Contains(rows[0]["grant_path"], `/Group, With "Comma"`) {
		t.Errorf("grant_path = %q, round trip failed", rows[0]["grant_path"])
	}
}

func TestWriteCSVDisambiguatesSameRoleNameOnDifferentClients(t *testing.T) {
	// Sorting on the visible name alone is ambiguous when two clients define the
	// same role, so client_id breaks the tie deterministically.
	res := &resolve.Result{
		Realm: "test",
		Grants: []resolve.Grant{
			{
				UserID: "u1", Username: "a.user", Enabled: true,
				Entitlement: model.ClientRole("zzz-app", "admin"),
				Path:        resolve.Path{Chain: []model.RoleKey{model.ClientRole("zzz-app", "admin")}},
				PathCount:   1,
			},
			{
				UserID: "u1", Username: "a.user", Enabled: true,
				Entitlement: model.ClientRole("aaa-app", "admin"),
				Path:        resolve.Path{Chain: []model.RoleKey{model.ClientRole("aaa-app", "admin")}},
				PathCount:   1,
			},
			{
				UserID: "u1", Username: "a.user", Enabled: true,
				Entitlement: model.RealmRole("admin"),
				Path:        resolve.Path{Chain: []model.RoleKey{model.RealmRole("admin")}},
				PathCount:   1,
			},
		},
	}

	_, rows := readCSV(t, render(t, res, output.Options{}))

	var got []string
	for _, r := range rows {
		got = append(got, r["entitlement"]+"@"+r["entitlement_type"]+"/"+r["client_id"])
	}
	want := []string{
		"admin@client_role/aaa-app",
		"admin@client_role/zzz-app",
		"admin@realm_role/",
	}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v\nwant      %v", got, want)
	}
}

func TestAccountClassUsesAuthoritativeFieldsNotJustTheUsername(t *testing.T) {
	// Regression: Grant carries only identity fields, so classifying from a Grant
	// alone silently labels every federated account "human" — the most common
	// account type in a real enterprise realm.
	res := &resolve.Result{
		Realm: "test",
		Users: []model.UserRepresentation{
			{ID: "u1", Username: "ldap.user", Email: "l@example.test", Enabled: true, FederationLink: "ldap-provider"},
			{ID: "u2", Username: "svc", Email: "s@example.test", Enabled: true, ServiceAccountClientLink: "c1"},
			{ID: "u3", Username: "p.person", Email: "p@example.test", Enabled: true},
		},
		Grants: []resolve.Grant{
			{UserID: "u1", Username: "ldap.user", Email: "l@example.test", Enabled: true,
				Entitlement: model.RealmRole("r"), Path: resolve.Path{Chain: []model.RoleKey{model.RealmRole("r")}}, PathCount: 1},
			{UserID: "u2", Username: "svc", Email: "s@example.test", Enabled: true,
				Entitlement: model.RealmRole("r"), Path: resolve.Path{Chain: []model.RoleKey{model.RealmRole("r")}}, PathCount: 1},
			{UserID: "u3", Username: "p.person", Email: "p@example.test", Enabled: true,
				Entitlement: model.RealmRole("r"), Path: resolve.Path{Chain: []model.RoleKey{model.RealmRole("r")}}, PathCount: 1},
		},
	}

	_, rows := readCSV(t, render(t, res, output.Options{}))

	want := map[string]string{
		"ldap.user": "federated",
		"svc":       "service_account",
		"p.person":  "human",
	}
	for _, r := range rows {
		if exp, ok := want[r["username"]]; ok && r["account_class"] != exp {
			t.Errorf("%s account_class = %q, want %q", r["username"], r["account_class"], exp)
		}
	}
}

func TestWriteCSVNeutralisesSpreadsheetFormulas(t *testing.T) {
	// A username or role name is enough to run a formula on the machine of
	// whoever opens the review. CSV quoting does not help: it is stripped before
	// the spreadsheet parses the cell.
	res := &resolve.Result{
		Realm: "test",
		Users: []model.UserRepresentation{
			{ID: "u1", Username: `=cmd|' /C calc'!A1`, Email: "x@y.z", Enabled: true},
		},
		Grants: []resolve.Grant{{
			UserID: "u1", Username: `=cmd|' /C calc'!A1`, Email: "x@y.z", Enabled: true,
			Entitlement: model.RealmRole(`=HYPERLINK("http://evil","ok")`),
			Path:        resolve.Path{Chain: []model.RoleKey{model.RealmRole("x")}},
			PathCount:   1,
		}},
	}

	raw := render(t, res, output.Options{})
	_, rows := readCSV(t, raw)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}

	for _, col := range []string{"username", "entitlement"} {
		got := rows[0][col]
		if strings.HasPrefix(got, "=") {
			t.Errorf("%s = %q, still executes as a formula when opened", col, got)
		}
		if !strings.HasPrefix(got, "'") {
			t.Errorf("%s = %q, want it marked as literal text", col, got)
		}
	}
}

func TestSanitiseOnlyTouchesDangerousCells(t *testing.T) {
	// Altering values is a real cost for an audit artefact, so it must happen
	// only where a spreadsheet would otherwise execute the cell.
	_, rows := readCSV(t, render(t, sampleResult(), output.Options{}))

	for _, r := range rows {
		for col, val := range r {
			if strings.HasPrefix(val, "'") {
				t.Errorf("ordinary value was altered: %s = %q", col, val)
			}
		}
	}

	// Every trigger character the major spreadsheets act on.
	for _, trigger := range []string{"=", "+", "-", "@", "\t", "\r"} {
		res := &resolve.Result{
			Realm: "test",
			Users: []model.UserRepresentation{{ID: "u1", Username: trigger + "danger", Enabled: true}},
			UsersWithoutGrants: []model.UserRepresentation{
				{ID: "u1", Username: trigger + "danger", Enabled: true},
			},
		}
		_, got := readCSV(t, render(t, res, output.Options{}))
		if len(got) != 1 {
			t.Fatalf("trigger %q: got %d rows", trigger, len(got))
		}
		if !strings.HasPrefix(got[0]["username"], "'") {
			t.Errorf("leading %q was not neutralised: %q", trigger, got[0]["username"])
		}
	}
}
