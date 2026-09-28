package kc

import (
	"context"
	"fmt"
	"net/url"

	"github.com/softika/kcac/internal/model"
)

// UserCount returns the number of users in the realm, used to size the work
// before starting and to report progress honestly.
func (c *Client) UserCount(ctx context.Context) (int, error) {
	var count int
	if err := c.getJSON(ctx, c.cfg.adminPath("/users/count"), &count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

// Users returns every user in the realm.
func (c *Client) Users(ctx context.Context) ([]model.UserRepresentation, error) {
	users, err := paged[model.UserRepresentation](ctx, c, c.cfg.adminPath("/users"), briefFalse())
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// UserGroups returns a user's DIRECT group memberships. Roles inherited from
// ancestors of these groups are not included here and must be resolved from the
// group tree.
func (c *Client) UserGroups(ctx context.Context, userID string) ([]model.GroupRepresentation, error) {
	groups, err := paged[model.GroupRepresentation](ctx, c, c.cfg.adminPath("/users/%s/groups", url.PathEscape(userID)), briefFalse())
	if err != nil {
		return nil, fmt.Errorf("list groups for user %s: %w", userID, err)
	}
	return groups, nil
}

// UserRoleMappings returns a user's direct realm and client role assignments.
// Composites are not expanded.
func (c *Client) UserRoleMappings(ctx context.Context, userID string) (model.MappingsRepresentation, error) {
	var out model.MappingsRepresentation
	if err := c.getJSON(ctx, c.cfg.adminPath("/users/%s/role-mappings", url.PathEscape(userID)), &out); err != nil {
		return model.MappingsRepresentation{}, fmt.Errorf("role mappings for user %s: %w", userID, err)
	}
	return out, nil
}

// GroupRoleMappings returns the roles a single group grants. Group role mappings
// are not part of the group representation, so this is a separate call per
// group.
func (c *Client) GroupRoleMappings(ctx context.Context, groupID string) (model.MappingsRepresentation, error) {
	var out model.MappingsRepresentation
	if err := c.getJSON(ctx, c.cfg.adminPath("/groups/%s/role-mappings", url.PathEscape(groupID)), &out); err != nil {
		return model.MappingsRepresentation{}, fmt.Errorf("role mappings for group %s: %w", groupID, err)
	}
	return out, nil
}

// RealmRoles returns every realm-level role.
func (c *Client) RealmRoles(ctx context.Context) ([]model.RoleRepresentation, error) {
	roles, err := paged[model.RoleRepresentation](ctx, c, c.cfg.adminPath("/roles"), briefFalse())
	if err != nil {
		return nil, fmt.Errorf("list realm roles: %w", err)
	}
	return roles, nil
}

// RealmRoleComposites returns the roles contained directly in a composite realm
// role. kcac expands the graph itself rather than calling the server's
// /composite endpoints, because those return a flat set with no path and the
// path is what a reviewer needs.
func (c *Client) RealmRoleComposites(ctx context.Context, roleName string) ([]model.RoleRepresentation, error) {
	roles, err := paged[model.RoleRepresentation](ctx, c, c.cfg.adminPath("/roles/%s/composites", url.PathEscape(roleName)), nil)
	if err != nil {
		return nil, fmt.Errorf("composites of realm role %s: %w", roleName, err)
	}
	return roles, nil
}

// Clients returns every client in the realm. The result maps the internal UUID
// used in API paths to the human-readable clientId used in output.
func (c *Client) Clients(ctx context.Context) ([]model.ClientRepresentation, error) {
	clients, err := paged[model.ClientRepresentation](ctx, c, c.cfg.adminPath("/clients"), nil)
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	return clients, nil
}

// ClientRoles returns every role defined on one client. clientUUID is the
// client's internal id, not its clientId.
func (c *Client) ClientRoles(ctx context.Context, clientUUID string) ([]model.RoleRepresentation, error) {
	roles, err := paged[model.RoleRepresentation](ctx, c, c.cfg.adminPath("/clients/%s/roles", url.PathEscape(clientUUID)), briefFalse())
	if err != nil {
		return nil, fmt.Errorf("list roles for client %s: %w", clientUUID, err)
	}
	return roles, nil
}

// ClientRoleComposites returns the roles contained directly in a composite
// client role.
func (c *Client) ClientRoleComposites(ctx context.Context, clientUUID, roleName string) ([]model.RoleRepresentation, error) {
	roles, err := paged[model.RoleRepresentation](ctx, c,
		c.cfg.adminPath("/clients/%s/roles/%s/composites", url.PathEscape(clientUUID), url.PathEscape(roleName)), nil)
	if err != nil {
		return nil, fmt.Errorf("composites of client role %s/%s: %w", clientUUID, roleName, err)
	}
	return roles, nil
}

// EffectiveRealmRoles returns Keycloak's own computation of a user's effective
// realm roles.
//
// kcac does NOT use this to build output: it is a flat set with no provenance.
// It exists as a test oracle — comparing it against kcac's computed set catches
// whole classes of resolution bug that hand-written expectations miss.
func (c *Client) EffectiveRealmRoles(ctx context.Context, userID string) ([]model.RoleRepresentation, error) {
	var roles []model.RoleRepresentation
	path := c.cfg.adminPath("/users/%s/role-mappings/realm/composite", url.PathEscape(userID))
	if err := c.getJSON(ctx, path, &roles); err != nil {
		return nil, fmt.Errorf("effective realm roles for user %s: %w", userID, err)
	}
	return roles, nil
}

// EffectiveClientRoles returns Keycloak's own computation of a user's effective
// roles for one client, composites recursed. clientUUID is the internal id.
//
// Like EffectiveRealmRoles this is a test oracle, not an output source: it
// returns a flat set with no provenance. Together the two cover both sides of the
// realm/client boundary that composite roles cross.
func (c *Client) EffectiveClientRoles(ctx context.Context, userID, clientUUID string) ([]model.RoleRepresentation, error) {
	var roles []model.RoleRepresentation
	path := c.cfg.adminPath("/users/%s/role-mappings/clients/%s/composite",
		url.PathEscape(userID), url.PathEscape(clientUUID))
	if err := c.getJSON(ctx, path, &roles); err != nil {
		return nil, fmt.Errorf("effective client roles for user %s on client %s: %w", userID, clientUUID, err)
	}
	return roles, nil
}

// ServerVersion reports the Keycloak version, best effort.
//
// /admin/serverinfo needs privileges beyond the read-only set kcac documents, so
// a failure here is expected and not an error: the version is recorded in the
// manifest when available and omitted when not. Behaviour never depends on it —
// kcac detects capabilities by probing endpoints instead.
func (c *Client) ServerVersion(ctx context.Context) string {
	var info struct {
		SystemInfo struct {
			Version string `json:"version"`
		} `json:"systemInfo"`
	}
	if err := c.getJSON(ctx, c.cfg.BaseURL+"/admin/serverinfo", &info); err != nil {
		return ""
	}
	return info.SystemInfo.Version
}
