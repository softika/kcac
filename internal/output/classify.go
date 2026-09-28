// Package output renders resolved access: the CSV a reviewer works from, the
// run manifest that records what was and was not knowable, and the human
// readable explanation of a single user's access.
package output

import (
	"strings"

	"github.com/softika/kcac/internal/model"
)

// AccountClass labels what kind of account a row describes, so a reviewer can
// tell a person from a machine before judging their access.
type AccountClass string

const (
	// AccountHuman means no marker suggests otherwise. It is an inference, not a
	// fact Keycloak records — Keycloak has no concept of a "human" account.
	AccountHuman AccountClass = "human"
	// AccountServiceAccount backs a confidential client.
	AccountServiceAccount AccountClass = "service_account"
	// AccountFederated is a shadow of an entry in an external directory. Its
	// attributes may be stale relative to that directory.
	AccountFederated AccountClass = "federated"
	// AccountUnknown means the record carried nothing to classify on. It is a
	// legitimate answer and is preferred to a confident guess.
	AccountUnknown AccountClass = "unknown"
)

// serviceAccountPrefix is how Keycloak names the user backing a client.
const serviceAccountPrefix = "service-account-"

// Classify labels an account.
//
// The heuristics are deliberately documented rather than tuned, because a
// reviewer who does not know how a label was derived cannot judge whether to
// trust it:
//
//  1. ServiceAccountClientLink set — authoritative, Keycloak records it.
//  2. FederationLink set — authoritative.
//  3. username begins with "service-account-" — a strong convention, used only
//     when the link is absent.
//  4. otherwise human, meaning no marker was found.
//
// An empty record classifies as unknown rather than as a human.
func Classify(u model.UserRepresentation) AccountClass {
	switch {
	case u.ID == "" && u.Username == "":
		return AccountUnknown
	case u.ServiceAccountClientLink != "":
		return AccountServiceAccount
	case u.FederationLink != "":
		return AccountFederated
	case strings.HasPrefix(u.Username, serviceAccountPrefix):
		return AccountServiceAccount
	default:
		return AccountHuman
	}
}
