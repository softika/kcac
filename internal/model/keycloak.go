package model

// The types below are the subset of Keycloak Admin API representations that kcac
// reads. Fields kcac does not use are deliberately omitted: a narrow struct is a
// narrower blast radius when the Admin API shape moves across major versions.
//
// Every one of these is populated from a read-only GET. kcac never writes.

// UserRepresentation is a Keycloak user.
//
// ServiceAccountClientLink and FederationLink are the honest basis for account
// classification: a populated ServiceAccountClientLink means the account backs a
// client, and a populated FederationLink means the account is a shadow of an
// external directory entry whose data may be stale.
type UserRepresentation struct {
	ID                       string `json:"id"`
	Username                 string `json:"username"`
	Email                    string `json:"email"`
	Enabled                  bool   `json:"enabled"`
	FederationLink           string `json:"federationLink"`
	ServiceAccountClientLink string `json:"serviceAccountClientLink"`
	CreatedTimestamp         int64  `json:"createdTimestamp"`
}

// GroupRepresentation is a Keycloak group.
//
// SubGroups is populated only by Keycloak versions before 23, where the group
// tree arrives nested in a single GET /groups response. From 23 onward children
// come from GET /groups/{id}/children and SubGroups is empty, so callers must
// treat an empty SubGroups as "unknown", never as "no children".
type GroupRepresentation struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Path      string                `json:"path"`
	ParentID  string                `json:"parentId"`
	SubGroups []GroupRepresentation `json:"subGroups"`
}

// RoleRepresentation is a Keycloak realm or client role.
//
// ContainerID is the realm name for a realm role and the client UUID for a
// client role, which is why resolving a client role to a legible clientId needs
// the client list as a lookup table.
type RoleRepresentation struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Composite   bool   `json:"composite"`
	ClientRole  bool   `json:"clientRole"`
	ContainerID string `json:"containerId"`
}

// ClientRepresentation is a Keycloak client.
//
// ID is the internal UUID used in API paths; ClientID is the human-readable name
// that belongs in reviewer-facing output.
type ClientRepresentation struct {
	ID       string `json:"id"`
	ClientID string `json:"clientId"`
	Enabled  bool   `json:"enabled"`
}

// MappingsRepresentation is the response of GET .../role-mappings for a user or
// a group. These are DIRECT assignments only — composites are not expanded and
// group inheritance is not applied, which is precisely why kcac computes the
// effective set itself.
type MappingsRepresentation struct {
	RealmMappings  []RoleRepresentation      `json:"realmMappings"`
	ClientMappings map[string]ClientMappings `json:"clientMappings"`
}

// ClientMappings holds one client's direct role assignments within a
// MappingsRepresentation.
type ClientMappings struct {
	ID       string               `json:"id"`
	Client   string               `json:"client"`
	Mappings []RoleRepresentation `json:"mappings"`
}

// Keys returns every directly assigned role in m as a RoleKey, realm roles
// first, then client roles. The returned slice is freshly allocated; m is not
// modified.
func (m MappingsRepresentation) Keys() []RoleKey {
	keys := make([]RoleKey, 0, len(m.RealmMappings))
	for _, r := range m.RealmMappings {
		keys = append(keys, RealmRole(r.Name))
	}
	for clientID, cm := range m.ClientMappings {
		// Prefer the map key: Keycloak sets it to the clientId, and it is
		// present even when the nested Client field is omitted.
		name := clientID
		if name == "" {
			name = cm.Client
		}
		for _, r := range cm.Mappings {
			keys = append(keys, ClientRole(name, r.Name))
		}
	}
	return keys
}
