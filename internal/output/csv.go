package output

import (
	"cmp"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/softika/kcac/internal/model"
	"github.com/softika/kcac/internal/resolve"
)

// Rows for an account holding nothing. Such an account is a review finding, not
// an absence of data, so it gets a row rather than being omitted.
const (
	noAccessPath = "(no access)"
	noAccessKind = "none"
)

// Header is the CSV column order. It is stable: a reviewer's spreadsheet and any
// downstream import depend on it.
var Header = []string{
	"user_id",
	"username",
	"email",
	"enabled",
	"entitlement",
	"entitlement_type",
	"client_id",
	"grant_path",
	"path_kind",
	"path_count",
	"account_class",
	"last_login",
}

// LastLogin carries what is known about login recency, including when nothing
// is known and why.
//
// The distinction between "did not log in" and "we cannot tell" is the whole
// point. Keycloak's event log is short-lived and frequently disabled outright,
// so an empty result means nothing on its own. Emitting "never" from absent
// events is a revocation recommendation built on no evidence.
type LastLogin struct {
	// Requested reports whether the caller asked for last-login data at all.
	Requested bool
	// Available reports whether it could actually be obtained.
	Available bool
	// Reason explains why it could not, when Available is false.
	Reason string
	// Window is how far back the event query reached.
	Window time.Duration
	// ByUserID holds the most recent login seen inside Window.
	ByUserID map[string]time.Time
}

// cell renders the last_login value for one user. It returns empty whenever the
// answer is unknown, and never substitutes a guess.
func (l LastLogin) cell(userID string) string {
	if !l.Requested || !l.Available {
		return ""
	}
	when, ok := l.ByUserID[userID]
	if !ok {
		// No login inside the window. That is not "never": the user may have
		// logged in before the window, or before retention expired.
		return ""
	}
	return when.UTC().Format(time.RFC3339)
}

// Options configures CSV rendering.
type Options struct {
	LastLogin LastLogin
}

// row is one CSV line, held in a struct so grants and no-access accounts can be
// merged into a single username-ordered listing.
type row struct {
	user        model.UserRepresentation
	entitlement model.RoleKey
	path        string
	kind        string
	pathCount   int
	hasAccess   bool
}

// sortKey orders rows the way the file reads left to right: username, then the
// visible entitlement name, then the columns that disambiguate it.
//
// Sorting on the internal RoleKey instead would group every client role before
// every realm role ("client:…" < "realm:…"), which is an ordering a reviewer
// scanning the entitlement column cannot see the logic of.
func (r row) sortKey() [4]string {
	return [4]string{
		r.user.Username,
		r.entitlement.Name,
		entitlementType(r),
		r.entitlement.Client,
	}
}

// WriteCSV renders a resolved result.
//
// Rows are ordered by username then entitlement, and accounts with no access are
// interleaved in the same order rather than appended, so the file reads as one
// pass over the realm and two review cycles diff cleanly.
func WriteCSV(w io.Writer, res *resolve.Result, opts Options) error {
	if res == nil {
		return fmt.Errorf("output: nil result")
	}

	// Classification needs the authoritative fields (FederationLink,
	// ServiceAccountClientLink) that a Grant does not carry, so look up the full
	// record. Falling back to the Grant's own fields keeps rendering correct for
	// a hand-built Result in tests.
	byID := make(map[string]model.UserRepresentation, len(res.Users))
	for _, u := range res.Users {
		byID[u.ID] = u
	}
	userFor := func(g resolve.Grant) model.UserRepresentation {
		if u, ok := byID[g.UserID]; ok {
			return u
		}
		return model.UserRepresentation{
			ID: g.UserID, Username: g.Username, Email: g.Email, Enabled: g.Enabled,
		}
	}

	rows := make([]row, 0, len(res.Grants)+len(res.UsersWithoutGrants))
	for _, g := range res.Grants {
		rows = append(rows, row{
			user:        userFor(g),
			entitlement: g.Entitlement,
			path:        g.Path.String(),
			kind:        string(g.Path.Kind()),
			pathCount:   g.PathCount,
			hasAccess:   true,
		})
	}
	for _, u := range res.UsersWithoutGrants {
		rows = append(rows, row{user: u, path: noAccessPath, kind: noAccessKind})
	}

	slices.SortStableFunc(rows, func(x, y row) int {
		a, b := x.sortKey(), y.sortKey()
		for k := range a {
			if c := cmp.Compare(a[k], b[k]); c != 0 {
				return c
			}
		}
		return 0
	})

	cw := csv.NewWriter(w)
	if err := cw.Write(Header); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}

	for _, r := range rows {
		record := []string{
			r.user.ID,
			r.user.Username,
			r.user.Email,
			strconv.FormatBool(r.user.Enabled),
			r.entitlement.Name,
			entitlementType(r),
			r.entitlement.Client,
			r.path,
			r.kind,
			pathCount(r),
			string(Classify(r.user)),
			opts.LastLogin.cell(r.user.ID),
		}
		// Applied to every column rather than the obviously risky ones: the
		// machine-generated columns are unaffected, and a uniform rule cannot be
		// forgotten when a column is added.
		for i := range record {
			record[i] = sanitizeCell(record[i])
		}
		if err := cw.Write(record); err != nil {
			return fmt.Errorf("write csv row for %s: %w", r.user.Username, err)
		}
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}
	return nil
}

// sanitizeCell neutralises text a spreadsheet would execute as a formula.
//
// encoding/csv quotes for delimiters, never for formulas: a cell beginning =, +,
// - or @ is evaluated by Excel, LibreOffice and Google Sheets when the file is
// opened, and CSV-level quoting is stripped before the cell is parsed, so it
// offers no protection at all.
//
// Almost every column here carries realm-controlled text. Keycloak's default
// username validator permits =, + and @, and role and group names are
// unrestricted, so a name is enough to run a formula on the machine of whoever
// opens the review. Prefixing an apostrophe marks the value as literal text.
//
// This is the one place kcac deliberately alters a value on its way out. It
// applies only to cells that would otherwise execute, which are conspicuous
// anyway, and it is documented in the README.
func sanitizeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func entitlementType(r row) string {
	if !r.hasAccess {
		return ""
	}
	return r.entitlement.EntitlementType()
}

func pathCount(r row) string {
	if !r.hasAccess {
		return "0"
	}
	return strconv.Itoa(r.pathCount)
}
