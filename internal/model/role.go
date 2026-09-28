// Package model holds the wire types kcac reads from the Keycloak Admin API
// and the domain types kcac computes from them.
package model

import (
	"fmt"
	"strings"
)

// RoleKey uniquely identifies a role across both realm and client scopes.
//
// Realm roles and client roles share one namespace in kcac because a composite
// realm role may contain client roles, so the composite graph spans both. Client
// is the human-readable clientId ("pos-app"), never the internal UUID, because
// every value that can reach a reviewer must be legible.
//
// RoleKey is a comparable value type and is safe to use as a map key.
type RoleKey struct {
	// Client is empty for a realm role, or the clientId for a client role.
	Client string
	// Name is the role name as defined in Keycloak.
	Name string
}

// RealmRole returns the key for a realm-level role.
func RealmRole(name string) RoleKey {
	return RoleKey{Name: name}
}

// ClientRole returns the key for a client-level role. clientID is the
// human-readable clientId, not the internal UUID.
func ClientRole(clientID, name string) RoleKey {
	return RoleKey{Client: clientID, Name: name}
}

// IsClientRole reports whether k identifies a client role.
func (k RoleKey) IsClientRole() bool { return k.Client != "" }

// Zero reports whether k is unset.
func (k RoleKey) Zero() bool { return k.Client == "" && k.Name == "" }

// String returns a stable, unambiguous rendering used for logging, manifest
// entries, JSON map keys and deterministic sorting. It is not the
// reviewer-facing form — grant paths are rendered separately.
func (k RoleKey) String() string {
	if k.IsClientRole() {
		// A colon in the clientId is escaped so the form stays unambiguous: the
		// first colon after the prefix always separates client from role name,
		// leaving role names free to contain colons themselves.
		return "client:" + escapeClient(k.Client) + ":" + k.Name
	}
	return "realm:" + k.Name
}

// colonEscape stands in for a literal colon inside the client segment, and
// percentEscape stands in for a literal percent sign.
const (
	colonEscape   = "%3A"
	percentEscape = "%25"
)

// escapeClient makes the client segment reversible.
//
// The percent sign must be escaped FIRST, otherwise the encoding has no unique
// inverse: clientId "a:b" and clientId "a%3Ab" would both render as "a%3Ab", and
// since RoleKey is a map key that silently merges two different roles into one,
// discarding one role's composite edges. Unescaping reverses the order.
func escapeClient(client string) string {
	client = strings.ReplaceAll(client, "%", percentEscape)
	return strings.ReplaceAll(client, ":", colonEscape)
}

// unescapeClient reverses escapeClient. Order matters: colons first, then
// percent signs.
func unescapeClient(client string) string {
	client = strings.ReplaceAll(client, colonEscape, ":")
	return strings.ReplaceAll(client, percentEscape, "%")
}

// MarshalText makes RoleKey usable as a JSON object key, so a snapshot can be
// written and read back. Replaying a snapshot through the resolver is how a bug
// report about a realm nobody else can reach becomes reproducible.
func (k RoleKey) MarshalText() ([]byte, error) {
	return []byte(k.String()), nil
}

// UnmarshalText parses the form produced by MarshalText.
func (k *RoleKey) UnmarshalText(text []byte) error {
	s := string(text)

	if rest, ok := strings.CutPrefix(s, "realm:"); ok {
		*k = RoleKey{Name: rest}
		return nil
	}
	if rest, ok := strings.CutPrefix(s, "client:"); ok {
		client, name, found := strings.Cut(rest, ":")
		if !found {
			return fmt.Errorf("invalid client role key %q: want client:<clientId>:<name>", s)
		}
		*k = RoleKey{Client: unescapeClient(client), Name: name}
		return nil
	}
	return fmt.Errorf("invalid role key %q: want realm:<name> or client:<clientId>:<name>", s)
}

// EntitlementType reports the CSV entitlement_type value for k.
func (k RoleKey) EntitlementType() string {
	if k.IsClientRole() {
		return "client_role"
	}
	return "realm_role"
}
