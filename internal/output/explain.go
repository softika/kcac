package output

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/softika/kcac/internal/model"
	"github.com/softika/kcac/internal/resolve"
)

// entitlementColumn is the width reserved for the entitlement name before its
// annotations, chosen so typical role names line up without truncation.
const entitlementColumn = 34

// WriteExplain renders every route by which one user holds every entitlement.
//
// The CSV shows the single simplest explanation per entitlement, because a
// reviewer facing the same role four times stops reading. This shows all of
// them, which is what you want when auditing one account or arguing with
// somebody about why a grant exists.
func WriteExplain(w io.Writer, res *resolve.Result, username string) error {
	if res == nil {
		return fmt.Errorf("output: nil result")
	}

	grants := grantsForUser(res, username)
	if len(grants) == 0 {
		return explainNoGrants(w, res, username)
	}

	out := &errWriter{w: w}
	g := grants[0]
	out.printf("%s\n", g.Username)
	out.printf("  %s\n\n", strings.Join(userAttributes(g, userRecord(res, g)), " · "))
	out.printf("  %d %s\n\n", len(grants), plural(len(grants), "entitlement", "entitlements"))

	for _, grant := range grants {
		out.printf("  %s  %s\n", pad(displayEntitlement(grant), entitlementColumn), annotations(grant))
		for i, p := range grant.AllPaths {
			branch := "├─"
			if i == len(grant.AllPaths)-1 {
				branch = "└─"
			}
			out.printf("    %s %s\n", branch, p)
		}
		out.printf("\n")
	}

	return out.err
}

// errWriter latches the first write failure so a long report does not have to
// check every call, and so a failure is reported rather than discarded.
//
// Silently succeeding on a full disk or a closed pipe would hand somebody a
// truncated access report that looks complete.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}

// explainNoGrants distinguishes "this account holds nothing" from "no such
// account", which are very different review outcomes.
func explainNoGrants(w io.Writer, res *resolve.Result, username string) error {
	for _, u := range res.UsersWithoutGrants {
		if u.Username == username {
			out := &errWriter{w: w}
			out.printf("%s\n", u.Username)
			out.printf("  %s · %s · %s\n\n", u.Email, enabledWord(u.Enabled), Classify(u))
			out.printf("  no entitlements\n")
			out.printf("\n  This account holds no access at all. That is a review finding in its\n")
			out.printf("  own right, not an absence of data.\n")
			return out.err
		}
	}
	return fmt.Errorf("no user %q in this realm", username)
}

func grantsForUser(res *resolve.Result, username string) []resolve.Grant {
	var out []resolve.Grant
	for _, g := range res.Grants {
		if g.Username == username {
			out = append(out, g)
		}
	}
	slices.SortStableFunc(out, func(a, b resolve.Grant) int {
		return cmp.Or(
			cmp.Compare(a.Entitlement.Name, b.Entitlement.Name),
			cmp.Compare(a.Entitlement.Client, b.Entitlement.Client),
		)
	})
	return out
}

// userRecord finds the full account record, which carries the fields
// classification depends on.
func userRecord(res *resolve.Result, g resolve.Grant) model.UserRepresentation {
	for _, u := range res.Users {
		if u.ID == g.UserID {
			return u
		}
	}
	return model.UserRepresentation{
		ID: g.UserID, Username: g.Username, Email: g.Email, Enabled: g.Enabled,
	}
}

func userAttributes(g resolve.Grant, u model.UserRepresentation) []string {
	attrs := make([]string, 0, 3)
	if g.Email != "" {
		attrs = append(attrs, g.Email)
	}
	attrs = append(attrs, enabledWord(g.Enabled), string(Classify(u)))
	return attrs
}

func displayEntitlement(g resolve.Grant) string {
	if g.Entitlement.IsClientRole() {
		return g.Entitlement.Client + ":" + g.Entitlement.Name
	}
	return g.Entitlement.Name
}

func annotations(g resolve.Grant) string {
	parts := []string{"realm role"}
	if g.Entitlement.IsClientRole() {
		parts = []string{"client role"}
	}
	if g.PathCount > 1 {
		parts = append(parts, fmt.Sprintf("%d paths", g.PathCount))
	}
	return strings.Join(parts, " · ")
}

func enabledWord(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "DISABLED"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// pad right-pads to a visible width.
//
// Counted in runes, not bytes: Keycloak does not restrict role or group names to
// ASCII, and a single accented character would otherwise under-pad and break the
// column this function exists to keep straight.
func pad(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}
