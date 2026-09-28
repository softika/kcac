package output

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/resolve"
)

// Manifest records what a run produced and, more importantly, what it could not
// know.
//
// CSV has no comment syntax, so provenance has to live beside the data rather
// than inside it. Limitations are recorded explicitly because a reviewer who
// trusts incomplete data is worse off than one who knows the data is partial.
type Manifest struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`

	Realm         string `json:"realm"`
	ServerVersion string `json:"server_version,omitempty"`
	// GroupChildrenEndpoint records which group-traversal path was used, since
	// it differs across Keycloak majors.
	GroupChildrenEndpoint string `json:"group_children_endpoint,omitempty"`

	CollectedAt time.Time `json:"collected_at"`
	ResolvedAt  time.Time `json:"resolved_at"`

	Counts  Counts      `json:"counts"`
	Output  OutputInfo  `json:"output"`
	Options ManifestOpt `json:"options"`

	// Limitations is every reason this run may be incomplete. An empty list is a
	// claim, so it is only empty when nothing was detected.
	Limitations []string `json:"limitations"`
}

// Counts summarises the realm as collected.
type Counts struct {
	Users              int `json:"users"`
	UsersWithoutAccess int `json:"users_without_access"`
	Grants             int `json:"grants"`
	Groups             int `json:"groups"`
	Clients            int `json:"clients"`
	RealmRoles         int `json:"realm_roles"`
	CompositeRoles     int `json:"composite_roles"`
}

// OutputInfo identifies the data file this manifest describes, so a bundle can
// be checked for tampering or truncation.
type OutputInfo struct {
	File   string `json:"file,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// ManifestOpt records the choices that shaped the output, so a reader can tell
// whether an omission was a bug or a setting.
type ManifestOpt struct {
	ExcludeDefaultRoles bool   `json:"exclude_default_roles"`
	LastLoginRequested  bool   `json:"last_login_requested"`
	LastLoginAvailable  bool   `json:"last_login_available"`
	LastLoginWindow     string `json:"last_login_window,omitempty"`
}

// BuildManifest assembles the record for a run. Output details are filled in by
// the caller once the data file has been written and hashed.
func BuildManifest(version string, snap *kc.Snapshot, res *resolve.Result, opts Options, excludeDefaults bool) Manifest {
	// WriteCSV and WriteExplain both refuse a nil result. This one cannot return
	// an error, so it records the problem instead of dereferencing nil.
	if res == nil {
		return Manifest{
			Tool:        "kcac",
			Version:     version,
			Limitations: []string{"no resolved result was available, so this manifest describes nothing"},
		}
	}

	m := Manifest{
		Tool:        "kcac",
		Version:     version,
		ResolvedAt:  res.ResolvedAt,
		Limitations: []string{},
		Options: ManifestOpt{
			ExcludeDefaultRoles: excludeDefaults,
			LastLoginRequested:  opts.LastLogin.Requested,
			LastLoginAvailable:  opts.LastLogin.Available,
		},
	}
	if opts.LastLogin.Requested && opts.LastLogin.Window > 0 {
		m.Options.LastLoginWindow = opts.LastLogin.Window.String()
	}

	if snap != nil {
		m.Realm = snap.Realm
		m.ServerVersion = snap.ServerVersion
		m.GroupChildrenEndpoint = snap.ChildrenEndpoint
		m.CollectedAt = snap.CollectedAt
		m.Counts = Counts{
			Users:          len(snap.Users),
			Groups:         len(snap.Groups),
			Clients:        len(snap.Clients),
			RealmRoles:     len(snap.RealmRoles),
			CompositeRoles: len(snap.Composites),
		}
		m.Limitations = append(m.Limitations, snap.Warnings...)
	}
	if m.Realm == "" {
		m.Realm = res.Realm
	}

	m.Counts.Grants = len(res.Grants)
	m.Counts.UsersWithoutAccess = len(res.UsersWithoutGrants)
	m.Limitations = append(m.Limitations, res.Warnings...)

	// Last-login state is a limitation whenever it was asked for and not
	// obtained, and a scope note even when it was.
	switch {
	case opts.LastLogin.Requested && !opts.LastLogin.Available:
		reason := opts.LastLogin.Reason
		if reason == "" {
			reason = "reason not recorded"
		}
		m.Limitations = append(m.Limitations,
			"last_login is empty for every account: "+reason)
	case opts.LastLogin.Requested:
		m.Limitations = append(m.Limitations, fmt.Sprintf(
			"last_login reflects only the %s before collection; an empty value means no login was recorded in that window, not that the account never logged in",
			opts.LastLogin.Window))
	default:
		m.Limitations = append(m.Limitations,
			"last_login was not requested and is empty for every account (pass --with-last-login)")
	}

	return m
}

// Encode renders the manifest as indented JSON.
//
// Named Encode rather than WriteTo so it does not shadow io.WriterTo, whose
// signature returns a byte count.
func (m Manifest) Encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}
