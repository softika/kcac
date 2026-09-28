package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/model"
	"github.com/softika/kcac/internal/output"
	"github.com/softika/kcac/internal/resolve"
)

func manifestJSON(t *testing.T, m output.Manifest) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := m.Encode(&buf); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, buf.String())
	}
	return out
}

func sampleSnapshot() *kc.Snapshot {
	return &kc.Snapshot{
		Realm:            "kcac-test",
		ServerVersion:    "26.0.8",
		ChildrenEndpoint: "supported",
		CollectedAt:      time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC),
		Users:            make([]model.UserRepresentation, 127),
		Groups:           make([]model.GroupRepresentation, 21),
		Clients:          make([]model.ClientRepresentation, 9),
		RealmRoles:       make([]model.RoleRepresentation, 8),
		Composites:       map[model.RoleKey][]model.RoleKey{model.RealmRole("a"): nil},
		Warnings:         []string{"server predates Keycloak 23: group children read from nested subGroups"},
	}
}

func TestManifestRecordsProvenanceAndCounts(t *testing.T) {
	res := &resolve.Result{Realm: "kcac-test", ResolvedAt: time.Now().UTC(), Grants: make([]resolve.Grant, 143)}
	m := output.BuildManifest("v0.1.0", sampleSnapshot(), res, output.Options{}, false)
	got := manifestJSON(t, m)

	if got["tool"] != "kcac" || got["version"] != "v0.1.0" {
		t.Errorf("tool/version = %v/%v", got["tool"], got["version"])
	}
	if got["realm"] != "kcac-test" || got["server_version"] != "26.0.8" {
		t.Errorf("realm/server = %v/%v", got["realm"], got["server_version"])
	}
	// Which traversal path was used matters when reproducing a result later.
	if got["group_children_endpoint"] != "supported" {
		t.Errorf("group_children_endpoint = %v", got["group_children_endpoint"])
	}

	counts := got["counts"].(map[string]any)
	for field, want := range map[string]float64{
		"users": 127, "groups": 21, "clients": 9, "realm_roles": 8, "composite_roles": 1, "grants": 143,
	} {
		if counts[field] != want {
			t.Errorf("counts.%s = %v, want %v", field, counts[field], want)
		}
	}
}

func TestManifestCarriesLimitationsForward(t *testing.T) {
	res := &resolve.Result{
		Realm:      "kcac-test",
		ResolvedAt: time.Now().UTC(),
		Warnings:   []string{"composite role cycle detected: a → b → a (expansion stopped at the repeat)"},
	}
	m := output.BuildManifest("v0.1.0", sampleSnapshot(), res, output.Options{}, false)

	joined := strings.Join(m.Limitations, "\n")
	// Both collection-time and resolution-time limits must survive into the
	// record: a reviewer cannot judge completeness from the CSV alone.
	if !strings.Contains(joined, "predates Keycloak 23") {
		t.Errorf("snapshot warning lost:\n%s", joined)
	}
	if !strings.Contains(joined, "cycle detected") {
		t.Errorf("resolver warning lost:\n%s", joined)
	}
}

func TestManifestLastLoginHonesty(t *testing.T) {
	res := &resolve.Result{Realm: "kcac-test", ResolvedAt: time.Now().UTC()}

	tests := []struct {
		name string
		opts output.Options
		want string
	}{
		{
			name: "not requested says so explicitly",
			opts: output.Options{},
			want: "was not requested",
		},
		{
			name: "requested but unavailable records the reason",
			opts: output.Options{LastLogin: output.LastLogin{
				Requested: true, Available: false,
				Reason: "event logging is disabled on this realm",
			}},
			want: "event logging is disabled on this realm",
		},
		{
			name: "available still states the window, because empty means unknown",
			opts: output.Options{LastLogin: output.LastLogin{
				Requested: true, Available: true, Window: 90 * 24 * time.Hour,
			}},
			want: "not that the account never logged in",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := output.BuildManifest("v0.1.0", nil, res, tt.opts, false)
			joined := strings.Join(m.Limitations, "\n")
			if !strings.Contains(joined, tt.want) {
				t.Errorf("limitations missing %q:\n%s", tt.want, joined)
			}
		})
	}
}

func TestManifestRecordsOptionsThatShapedOutput(t *testing.T) {
	// An omission caused by a flag must be distinguishable from a bug.
	res := &resolve.Result{Realm: "kcac-test", ResolvedAt: time.Now().UTC()}
	m := output.BuildManifest("v0.1.0", nil, res, output.Options{}, true)

	got := manifestJSON(t, m)
	opts := got["options"].(map[string]any)
	if opts["exclude_default_roles"] != true {
		t.Errorf("exclude_default_roles = %v, want true", opts["exclude_default_roles"])
	}
}

func TestManifestOutputInfoIsSettable(t *testing.T) {
	res := &resolve.Result{Realm: "kcac-test", ResolvedAt: time.Now().UTC()}
	m := output.BuildManifest("v0.1.0", nil, res, output.Options{}, false)
	m.Output = output.OutputInfo{File: "access.csv", Bytes: 4096, SHA256: "abc123"}

	got := manifestJSON(t, m)
	out := got["output"].(map[string]any)
	if out["file"] != "access.csv" || out["sha256"] != "abc123" || out["bytes"] != float64(4096) {
		t.Errorf("output info = %v", out)
	}
}

func TestManifestLimitationsIsNeverNull(t *testing.T) {
	// An empty list is a claim that nothing was detected; null would be ambiguous
	// and would break consumers expecting an array.
	res := &resolve.Result{Realm: "x", ResolvedAt: time.Now().UTC()}
	m := output.BuildManifest("v0.1.0", nil, res, output.Options{}, false)
	got := manifestJSON(t, m)
	if _, ok := got["limitations"].([]any); !ok {
		t.Errorf("limitations = %#v, want a JSON array", got["limitations"])
	}
}

func TestBuildManifestHandlesNilResult(t *testing.T) {
	// WriteCSV and WriteExplain both refuse a nil result. This cannot return an
	// error, so it must say so rather than panic.
	m := output.BuildManifest("v0.1.0", nil, nil, output.Options{}, false)

	if m.Tool != "kcac" || m.Version != "v0.1.0" {
		t.Errorf("manifest lost its identity: %+v", m)
	}
	if len(m.Limitations) == 0 {
		t.Error("a manifest describing nothing must say so")
	}
}
