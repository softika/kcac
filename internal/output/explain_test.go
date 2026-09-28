package output_test

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/softika/kcac/internal/model"
	"github.com/softika/kcac/internal/output"
	"github.com/softika/kcac/internal/resolve"
)

func explainResult() *resolve.Result {
	storeAdmin := model.RealmRole("store-admin")
	return &resolve.Result{
		Realm: "test",
		Users: []model.UserRepresentation{
			{ID: "u1", Username: "d.dual", Email: "d.dual@example.test", Enabled: true},
			{ID: "u2", Username: "n.nobody", Email: "n@example.test", Enabled: true},
			{ID: "u3", Username: "x.disabled", Email: "x@example.test", Enabled: false, FederationLink: "ldap"},
		},
		Grants: []resolve.Grant{
			{
				UserID: "u1", Username: "d.dual", Email: "d.dual@example.test", Enabled: true,
				Entitlement: storeAdmin,
				Path:        resolve.Path{Chain: []model.RoleKey{storeAdmin}},
				PathCount:   2,
				AllPaths: []resolve.Path{
					{Chain: []model.RoleKey{storeAdmin}},
					{MemberGroup: "/Retail/StoreManagers", GrantGroup: "/Retail/StoreManagers", Chain: []model.RoleKey{storeAdmin}},
				},
			},
			{
				UserID: "u1", Username: "d.dual", Email: "d.dual@example.test", Enabled: true,
				Entitlement: model.ClientRole("pos-app", "till-operator"),
				Path: resolve.Path{
					Chain: []model.RoleKey{storeAdmin, model.ClientRole("pos-app", "till-operator")},
				},
				PathCount: 1,
				AllPaths: []resolve.Path{
					{Chain: []model.RoleKey{storeAdmin, model.ClientRole("pos-app", "till-operator")}},
				},
			},
			{
				UserID: "u3", Username: "x.disabled", Email: "x@example.test", Enabled: false,
				Entitlement: model.RealmRole("base-employee"),
				Path:        resolve.Path{Chain: []model.RoleKey{model.RealmRole("base-employee")}},
				PathCount:   1,
				AllPaths:    []resolve.Path{{Chain: []model.RoleKey{model.RealmRole("base-employee")}}},
			},
		},
		UsersWithoutGrants: []model.UserRepresentation{
			{ID: "u2", Username: "n.nobody", Email: "n@example.test", Enabled: true},
		},
	}
}

func explain(t *testing.T, username string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := output.WriteExplain(&buf, explainResult(), username); err != nil {
		t.Fatalf("WriteExplain(%q) error = %v", username, err)
	}
	return buf.String()
}

func TestExplainShowsEveryPathNotJustTheChosenOne(t *testing.T) {
	got := explain(t, "d.dual")

	// The CSV shows one explanation per entitlement; explain exists to show all
	// of them, which is what you need when auditing a single account.
	if !strings.Contains(got, "direct assignment") {
		t.Errorf("missing the direct path:\n%s", got)
	}
	if !strings.Contains(got, "via group /Retail/StoreManagers") {
		t.Errorf("missing the group path:\n%s", got)
	}
	if !strings.Contains(got, "2 paths") {
		t.Errorf("path count not annotated:\n%s", got)
	}
	// Tree branches: all but the last use a tee.
	if !strings.Contains(got, "├─") || !strings.Contains(got, "└─") {
		t.Errorf("paths are not rendered as a tree:\n%s", got)
	}
}

func TestExplainRendersHeaderAndComposites(t *testing.T) {
	got := explain(t, "d.dual")

	for _, want := range []string{
		"d.dual",
		"d.dual@example.test",
		"enabled",
		"human",
		"2 entitlements",
		"pos-app:till-operator",
		"client role",
		"direct assignment of store-admin → composite pos-app:till-operator",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestExplainMarksDisabledAndFederatedAccounts(t *testing.T) {
	got := explain(t, "x.disabled")

	// A disabled account holding standing access is exactly what a review is
	// looking for, so the state must be prominent rather than a quiet "false".
	if !strings.Contains(got, "DISABLED") {
		t.Errorf("disabled state not prominent:\n%s", got)
	}
	if !strings.Contains(got, "federated") {
		t.Errorf("federated classification missing:\n%s", got)
	}
}

func TestExplainDistinguishesNoAccessFromNoSuchUser(t *testing.T) {
	got := explain(t, "n.nobody")
	if !strings.Contains(got, "no entitlements") {
		t.Errorf("account with no access not reported clearly:\n%s", got)
	}
	if !strings.Contains(got, "review finding") {
		t.Errorf("output should say an empty account is itself a finding:\n%s", got)
	}

	var buf bytes.Buffer
	err := output.WriteExplain(&buf, explainResult(), "ghost")
	if err == nil {
		t.Fatal("explaining an unknown user should error, not print an empty report")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should name the user", err)
	}
}

func TestExplainNilResult(t *testing.T) {
	var buf bytes.Buffer
	if err := output.WriteExplain(&buf, nil, "anyone"); err == nil {
		t.Error("WriteExplain(nil) should error")
	}
}

func TestExplainAlignsNonASCIINames(t *testing.T) {
	// Keycloak does not restrict role names to ASCII. Padding by byte length
	// would under-pad a name with accented or CJK characters and break the
	// column the layout depends on.
	res := &resolve.Result{
		Realm: "test",
		Users: []model.UserRepresentation{{ID: "u1", Username: "u", Enabled: true}},
		Grants: []resolve.Grant{
			{
				UserID: "u1", Username: "u", Enabled: true,
				Entitlement: model.RealmRole("zahlungsprüfer-öst"),
				Path:        resolve.Path{Chain: []model.RoleKey{model.RealmRole("zahlungsprüfer-öst")}},
				PathCount:   1,
				AllPaths:    []resolve.Path{{Chain: []model.RoleKey{model.RealmRole("zahlungsprüfer-öst")}}},
			},
			{
				UserID: "u1", Username: "u", Enabled: true,
				Entitlement: model.RealmRole("aaaaaaaaaaaaaaaaaa"), // same rune count
				Path:        resolve.Path{Chain: []model.RoleKey{model.RealmRole("aaaaaaaaaaaaaaaaaa")}},
				PathCount:   1,
				AllPaths:    []resolve.Path{{Chain: []model.RoleKey{model.RealmRole("aaaaaaaaaaaaaaaaaa")}}},
			},
		},
	}

	var buf bytes.Buffer
	if err := output.WriteExplain(&buf, res, "u"); err != nil {
		t.Fatalf("WriteExplain() error = %v", err)
	}

	// Both names are 18 characters, so the annotation must start at the same
	// visible column on both lines.
	var cols []int
	for _, line := range strings.Split(buf.String(), "\n") {
		if idx := strings.Index(line, "realm role"); idx >= 0 && strings.HasPrefix(line, "  ") {
			cols = append(cols, utf8.RuneCountInString(line[:idx]))
		}
	}
	if len(cols) != 2 {
		t.Fatalf("expected 2 entitlement lines, got %d:\n%s", len(cols), buf.String())
	}
	if cols[0] != cols[1] {
		t.Errorf("columns misaligned: %d vs %d (padding counted bytes, not characters)\n%s",
			cols[0], cols[1], buf.String())
	}
}
