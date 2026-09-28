package kc

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/softika/kcac/internal/model"
)

// Snapshot is everything kcac reads from one realm at one point in time.
//
// The json tags are load-bearing: `kcac fetch` writes this verbatim, and that
// file is what somebody attaches to a bug report about a realm nobody else can
// reach. Tying the wire format to Go field names would let a rename silently
// break every tool that reads it.
//
// It is deliberately raw: direct assignments only, no composite expansion and no
// group inheritance applied. Resolution happens in internal/resolve, which takes
// a Snapshot and performs no I/O — so the correctness bar is testable by
// constructing a Snapshot literal, with no Keycloak involved.
type Snapshot struct {
	Realm         string    `json:"realm"`
	CollectedAt   time.Time `json:"collected_at"`
	ServerVersion string    `json:"server_version,omitempty"`

	// ChildrenEndpoint records what capability detection concluded, for the
	// manifest: "supported", "unsupported" or "unknown".
	ChildrenEndpoint string `json:"group_children_endpoint"`

	Users   []model.UserRepresentation   `json:"users"`
	Groups  []model.GroupRepresentation  `json:"groups"`
	Clients []model.ClientRepresentation `json:"clients"`

	// RealmRoles and ClientRoles are the role catalogue, kept so output can
	// report roles that exist but are assigned to nobody.
	RealmRoles  []model.RoleRepresentation            `json:"realm_roles"`
	ClientRoles map[string][]model.RoleRepresentation `json:"client_roles,omitempty"`

	// UserGroupIDs maps a user id to their DIRECT group memberships. Ancestor
	// groups are not included; resolve derives those from Groups.
	UserGroupIDs map[string][]string `json:"user_group_ids,omitempty"`
	// UserRoles maps a user id to their directly assigned roles.
	UserRoles map[string][]model.RoleKey `json:"user_roles,omitempty"`
	// GroupRoles maps a group id to the roles that group grants directly.
	GroupRoles map[string][]model.RoleKey `json:"group_roles,omitempty"`
	// Composites maps a composite role to the roles it contains directly. Realm
	// and client roles share the keyspace because a realm composite may contain
	// client roles.
	Composites map[model.RoleKey][]model.RoleKey `json:"composites,omitempty"`

	// Warnings records anything that limits the completeness of this snapshot.
	// It is surfaced in the manifest rather than hidden, because a reviewer
	// trusting incomplete data is worse than a reviewer who knows it is partial.
	Warnings []string `json:"warnings"`
}

// Progress reports collection milestones. Implementations write to stderr so
// stdout stays a clean data stream.
type Progress func(format string, args ...any)

// Collect reads a whole realm.
//
// Work is fanned out with bounded concurrency; the Client's own semaphore caps
// in-flight requests, so a large realm is collected steadily rather than in a
// burst that degrades the customer's server.
func Collect(ctx context.Context, c *Client, progress Progress) (*Snapshot, error) {
	if progress == nil {
		progress = func(string, ...any) {}
	}

	snap := &Snapshot{
		Realm:        c.cfg.Realm,
		CollectedAt:  time.Now().UTC(),
		UserGroupIDs: make(map[string][]string),
		UserRoles:    make(map[string][]model.RoleKey),
		GroupRoles:   make(map[string][]model.RoleKey),
		Composites:   make(map[model.RoleKey][]model.RoleKey),
	}

	count, err := c.UserCount(ctx)
	if err != nil {
		return nil, err
	}
	progress("realm %q: %d users", c.cfg.Realm, count)

	clients, err := c.Clients(ctx)
	if err != nil {
		return nil, err
	}
	snap.Clients = clients
	uuidToClientID := make(map[string]string, len(clients))
	clientIDToUUID := make(map[string]string, len(clients))
	for _, cl := range clients {
		uuidToClientID[cl.ID] = cl.ClientID
		clientIDToUUID[cl.ClientID] = cl.ID
	}
	progress("clients: %d", len(clients))

	if err := collectRoles(ctx, c, snap, uuidToClientID, clientIDToUUID, progress); err != nil {
		return nil, err
	}

	if err := collectGroups(ctx, c, snap, progress); err != nil {
		return nil, err
	}

	if err := collectUsers(ctx, c, snap, progress); err != nil {
		return nil, err
	}

	known, supported := c.SupportsGroupChildren()
	switch {
	case !known:
		snap.ChildrenEndpoint = "unknown"
	case supported:
		snap.ChildrenEndpoint = "supported"
	default:
		snap.ChildrenEndpoint = "unsupported"
		snap.Warnings = append(snap.Warnings,
			"server predates Keycloak 23: group children read from nested subGroups")
	}

	snap.ServerVersion = c.ServerVersion(ctx)
	if snap.ServerVersion == "" {
		snap.Warnings = append(snap.Warnings,
			"server version unavailable (/admin/serverinfo needs privileges beyond the read-only set); capability detection was used instead")
	}

	return snap, nil
}

// collectRoles gathers realm roles, client roles, and the composite edges
// between them.
func collectRoles(ctx context.Context, c *Client, snap *Snapshot,
	uuidToClientID, clientIDToUUID map[string]string, progress Progress) error {

	realmRoles, err := c.RealmRoles(ctx)
	if err != nil {
		return err
	}
	snap.RealmRoles = realmRoles

	snap.ClientRoles = make(map[string][]model.RoleRepresentation, len(snap.Clients))
	// composite holds every role that declares itself composite, so its edges
	// can be fetched concurrently below.
	type target struct {
		key  model.RoleKey
		uuid string // client uuid, empty for realm roles
	}
	var composites []target

	for _, r := range realmRoles {
		if r.Composite {
			composites = append(composites, target{key: model.RealmRole(r.Name)})
		}
	}

	for _, cl := range snap.Clients {
		roles, err := c.ClientRoles(ctx, cl.ID)
		if err != nil {
			return err
		}
		if len(roles) > 0 {
			snap.ClientRoles[cl.ClientID] = roles
		}
		for _, r := range roles {
			if r.Composite {
				composites = append(composites, target{key: model.ClientRole(cl.ClientID, r.Name), uuid: cl.ID})
			}
		}
	}
	progress("roles: %d realm, %d composite", len(realmRoles), len(composites))

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)

	for _, t := range composites {
		g.Go(func() error {
			var (
				contained []model.RoleRepresentation
				err       error
			)
			if t.key.IsClientRole() {
				contained, err = c.ClientRoleComposites(gctx, t.uuid, t.key.Name)
			} else {
				contained, err = c.RealmRoleComposites(gctx, t.key.Name)
			}
			if err != nil {
				return err
			}

			edges := make([]model.RoleKey, 0, len(contained))
			for _, r := range contained {
				edges = append(edges, roleKeyOf(r, uuidToClientID))
			}

			mu.Lock()
			snap.Composites[t.key] = edges
			mu.Unlock()
			return nil
		})
	}
	return g.Wait()
}

// collectGroups reads the group tree and each group's own role grants.
func collectGroups(ctx context.Context, c *Client, snap *Snapshot, progress Progress) error {

	groups, err := c.GroupTree(ctx)
	if err != nil {
		return err
	}
	snap.Groups = groups
	progress("groups: %d", len(groups))

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)

	for _, grp := range groups {
		g.Go(func() error {
			mappings, err := c.GroupRoleMappings(gctx, grp.ID)
			if err != nil {
				return err
			}
			keys := sortedRoleKeys(mappings)
			if len(keys) == 0 {
				return nil
			}
			mu.Lock()
			snap.GroupRoles[grp.ID] = keys
			mu.Unlock()
			return nil
		})
	}
	return g.Wait()
}

// collectUsers reads every user with their direct groups and direct roles.
func collectUsers(ctx context.Context, c *Client, snap *Snapshot, progress Progress) error {

	users, err := c.Users(ctx)
	if err != nil {
		return err
	}
	snap.Users = users
	progress("fetched %d users, resolving memberships", len(users))

	var (
		mu   sync.Mutex
		done int
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Concurrency)

	for _, u := range users {
		g.Go(func() error {
			groups, err := c.UserGroups(gctx, u.ID)
			if err != nil {
				return err
			}
			mappings, err := c.UserRoleMappings(gctx, u.ID)
			if err != nil {
				return err
			}

			groupIDs := make([]string, 0, len(groups))
			for _, grp := range groups {
				groupIDs = append(groupIDs, grp.ID)
			}
			keys := sortedRoleKeys(mappings)

			mu.Lock()
			if len(groupIDs) > 0 {
				snap.UserGroupIDs[u.ID] = groupIDs
			}
			if len(keys) > 0 {
				snap.UserRoles[u.ID] = keys
			}
			done++
			if done%500 == 0 {
				progress("  %d/%d users", done, len(users))
			}
			mu.Unlock()
			return nil
		})
	}
	return g.Wait()
}

// roleKeyOf converts a role representation to a RoleKey, translating a client
// role's ContainerID (a UUID) into the human-readable clientId.
func roleKeyOf(r model.RoleRepresentation, uuidToClientID map[string]string) model.RoleKey {
	if !r.ClientRole {
		return model.RealmRole(r.Name)
	}
	clientID, ok := uuidToClientID[r.ContainerID]
	if !ok {
		// Unknown container: keep the UUID rather than silently demoting a
		// client role to a realm role, which would misreport its scope.
		clientID = r.ContainerID
	}
	return model.ClientRole(clientID, r.Name)
}

// sortedRoleKeys converts a mappings response to RoleKeys in a stable order.
//
// The conversion itself lives on the representation, which already keys its
// client mappings by clientId, so no UUID lookup is needed here.
func sortedRoleKeys(m model.MappingsRepresentation) []model.RoleKey {
	keys := m.Keys()
	slices.SortFunc(keys, func(a, b model.RoleKey) int {
		return cmp.Compare(a.String(), b.String())
	})
	return keys
}

// Stats summarises a snapshot for progress output and the manifest.
func (s *Snapshot) Stats() string {
	return fmt.Sprintf("%d users, %d groups, %d clients, %d realm roles, %d composite roles",
		len(s.Users), len(s.Groups), len(s.Clients), len(s.RealmRoles), len(s.Composites))
}
