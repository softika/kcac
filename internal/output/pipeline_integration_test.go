//go:build integration

// End-to-end: collect a real realm, resolve it, render it, and assert on the
// bytes a reviewer would actually receive.
//
//	docker compose -f test/docker-compose.yml up -d && ./test/wait-for-keycloak.sh
//	go test -tags=integration -count=1 ./internal/output/
package output_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/output"
	"github.com/softika/kcac/internal/resolve"
)

// uuidPattern matches Keycloak's internal ids in their canonical 8-4-4-4-12 form.
var uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func livePipeline(t *testing.T) (*kc.Snapshot, *resolve.Result) {
	t.Helper()

	url := os.Getenv("KCAC_TEST_URL")
	if url == "" {
		t.Skip("KCAC_TEST_URL not set; start test/docker-compose.yml to run pipeline tests")
	}
	cfg := kc.Config{
		BaseURL:      url,
		Realm:        envOrDefault("KCAC_TEST_REALM", "kcac-test"),
		ClientID:     envOrDefault("KCAC_TEST_CLIENT_ID", "kcac-audit"),
		ClientSecret: envOrDefault("KCAC_CLIENT_SECRET", "test-secret"),
	}
	client, err := kc.New(cfg)
	if err != nil {
		t.Fatalf("kc.New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	snap, err := kc.Collect(ctx, client, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	return snap, resolve.Resolve(snap, resolve.Options{})
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestPipelineCSVCarriesProvenanceForRealUsers(t *testing.T) {
	_, res := livePipeline(t)

	var buf bytes.Buffer
	if err := output.WriteCSV(&buf, res, output.Options{}); err != nil {
		t.Fatalf("WriteCSV() error = %v", err)
	}
	recs, err := csv.NewReader(bytes.NewReader(buf.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("rendered output is not valid CSV: %v", err)
	}

	// column index by name, so the assertions survive a column reorder.
	col := map[string]int{}
	for i, h := range recs[0] {
		col[h] = i
	}

	find := func(username, entitlement string) []string {
		for _, r := range recs[1:] {
			if r[col["username"]] == username && r[col["entitlement"]] == entitlement {
				return r
			}
		}
		t.Fatalf("no CSV row for %s / %s", username, entitlement)
		return nil
	}

	// The flagship case, asserted on the rendered bytes rather than on an
	// in-memory struct: a.gruber holds no direct roles at all, and the file must
	// say exactly why she has this one.
	got := find("a.gruber", "base-employee")[col["grant_path"]]
	if want := "via group /Retail/StoreManagers (inherited from /Retail)"; got != want {
		t.Errorf("a.gruber base-employee grant_path = %q\nwant %q", got, want)
	}

	// Three levels of provenance in one cell: member group, the ancestor that
	// granted the role, and the composite expansion that followed.
	got = find("s.novak", "till-supervisor")[col["grant_path"]]
	for _, want := range []string{
		"via group /Retail/StoreManagers/Region-East",
		"inherited from /Retail/StoreManagers",
		"composite store-admin → till-supervisor",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("s.novak till-supervisor grant_path = %q\nmissing %q", got, want)
		}
	}

	// The realm/client boundary crossing must render with a legible client id.
	row := find("m.huber", "till-operator")
	if row[col["client_id"]] != "pos-app" || row[col["entitlement_type"]] != "client_role" {
		t.Errorf("m.huber till-operator: client_id=%q type=%q", row[col["client_id"]], row[col["entitlement_type"]])
	}

	// No cell may leak an internal id into reviewer-facing text: nobody can
	// certify f47ac10b-58cc-4372-a567-0e02b2c3d479.
	//
	// Matched as a real UUID rather than by counting hyphens — a legitimate path
	// like "regional-manager → store-admin → till-supervisor" is full of them.
	for _, r := range recs[1:] {
		for _, field := range []string{"entitlement", "grant_path", "client_id"} {
			if uuidPattern.MatchString(r[col[field]]) {
				t.Errorf("%s leaks a UUID in reviewer-facing column %s: %q",
					r[col["username"]], field, r[col[field]])
			}
		}
	}
}

func TestPipelineAccountWithNoAccessSurvivesToTheFile(t *testing.T) {
	_, res := livePipeline(t)

	var buf bytes.Buffer
	if err := output.WriteCSV(&buf, res, output.Options{}); err != nil {
		t.Fatalf("WriteCSV() error = %v", err)
	}

	if !strings.Contains(buf.String(), "n.nobody") {
		t.Error("n.nobody holds no access and was dropped from the CSV; such an account is a review finding")
	}
	if !strings.Contains(buf.String(), "(no access)") {
		t.Error("no-access rows are not labelled")
	}
}

func TestPipelineManifestDescribesTheRun(t *testing.T) {
	snap, res := livePipeline(t)

	m := output.BuildManifest("test", snap, res, output.Options{}, false)
	if m.Realm == "" || m.Counts.Users == 0 || m.Counts.Grants == 0 {
		t.Errorf("manifest is missing basic provenance: %+v", m)
	}
	// Capability detection must be recorded: it is the difference between two
	// code paths and matters when reproducing a result later.
	if m.GroupChildrenEndpoint != "supported" && m.GroupChildrenEndpoint != "unsupported" {
		t.Errorf("group_children_endpoint = %q, want a definite answer", m.GroupChildrenEndpoint)
	}
	// An honest run always states the last-login position.
	var mentions bool
	for _, l := range m.Limitations {
		if strings.Contains(l, "last_login") {
			mentions = true
		}
	}
	if !mentions {
		t.Errorf("manifest never mentions last_login: %v", m.Limitations)
	}
}

func TestPipelineExplainRendersForARealUser(t *testing.T) {
	_, res := livePipeline(t)

	var buf bytes.Buffer
	if err := output.WriteExplain(&buf, res, "d.dual"); err != nil {
		t.Fatalf("WriteExplain() error = %v", err)
	}
	got := buf.String()

	// d.dual holds store-admin directly AND through a group, so explain must show
	// both rather than the single one the CSV picks.
	if !strings.Contains(got, "direct assignment") || !strings.Contains(got, "via group /Retail/StoreManagers") {
		t.Errorf("explain did not show both routes:\n%s", got)
	}
	if !strings.Contains(got, "paths") {
		t.Errorf("explain did not annotate the path count:\n%s", got)
	}
}
