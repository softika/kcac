package resolve_test

import (
	"fmt"
	"strings"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/model"
)

// builder assembles a kc.Snapshot by hand so resolution cases read as realm
// shapes rather than as fixture plumbing. No Keycloak is involved: resolution is
// pure, which is the whole reason the correctness bar is unit-testable.
type builder struct {
	snap    *kc.Snapshot
	groupID map[string]string // group path -> synthetic id
	userID  map[string]string // username -> synthetic id
}

func newSnapshot() *builder {
	return &builder{
		snap: &kc.Snapshot{
			Realm:        "test",
			UserGroupIDs: map[string][]string{},
			UserRoles:    map[string][]model.RoleKey{},
			GroupRoles:   map[string][]model.RoleKey{},
			Composites:   map[model.RoleKey][]model.RoleKey{},
		},
		groupID: map[string]string{},
		userID:  map[string]string{},
	}
}

// group adds a group at the given path, inferring its parent from the path, and
// grants it the given roles directly.
func (b *builder) group(path string, roles ...model.RoleKey) *builder {
	id := fmt.Sprintf("g%d", len(b.groupID)+1)
	b.groupID[path] = id

	parentPath := path[:strings.LastIndex(path, "/")]
	parentID := ""
	if parentPath != "" {
		var ok bool
		parentID, ok = b.groupID[parentPath]
		if !ok {
			panic("builder: parent group " + parentPath + " must be added before " + path)
		}
	}

	b.snap.Groups = append(b.snap.Groups, model.GroupRepresentation{
		ID:       id,
		Name:     path[strings.LastIndex(path, "/")+1:],
		Path:     path,
		ParentID: parentID,
	})
	if len(roles) > 0 {
		b.snap.GroupRoles[id] = roles
	}
	return b
}

// composite records that parent contains children directly.
func (b *builder) composite(parent model.RoleKey, children ...model.RoleKey) *builder {
	b.snap.Composites[parent] = append(b.snap.Composites[parent], children...)
	return b
}

// user adds a user with direct group memberships (by path) and direct roles.
func (b *builder) user(username string, groups []string, roles ...model.RoleKey) *builder {
	id := fmt.Sprintf("u%d", len(b.userID)+1)
	b.userID[username] = id

	b.snap.Users = append(b.snap.Users, model.UserRepresentation{
		ID:       id,
		Username: username,
		Email:    username + "@example.test",
		Enabled:  true,
	})
	for _, path := range groups {
		gid, ok := b.groupID[path]
		if !ok {
			panic("builder: unknown group " + path)
		}
		b.snap.UserGroupIDs[id] = append(b.snap.UserGroupIDs[id], gid)
	}
	if len(roles) > 0 {
		b.snap.UserRoles[id] = roles
	}
	return b
}

// disabled marks the most recently added user as disabled.
func (b *builder) disabled() *builder {
	b.snap.Users[len(b.snap.Users)-1].Enabled = false
	return b
}

func (b *builder) build() *kc.Snapshot { return b.snap }

// retailRealm is the shared shape most cases use. Roles are granted at every
// level of a three-deep tree, and store-admin is a composite that crosses into
// client roles:
//
//	/Retail                             -> base-employee
//	/Retail/StoreManagers               -> store-admin
//	/Retail/StoreManagers/Region-East   -> pos-app:till-admin
//	/HQ                                 -> regional-manager
//
//	regional-manager -> store-admin -> {till-supervisor, pos-app:till-operator}
//	till-supervisor  -> base-employee
//	pos-app:till-admin -> pos-app:till-operator
func retailRealm() *builder {
	return newSnapshot().
		group("/Retail", model.RealmRole("base-employee")).
		group("/Retail/StoreManagers", model.RealmRole("store-admin")).
		group("/Retail/StoreManagers/Region-East", model.ClientRole("pos-app", "till-admin")).
		group("/Retail/StoreManagers/Region-West").
		group("/HQ", model.RealmRole("regional-manager")).
		composite(model.RealmRole("regional-manager"), model.RealmRole("store-admin")).
		composite(model.RealmRole("store-admin"), model.RealmRole("till-supervisor"), model.ClientRole("pos-app", "till-operator")).
		composite(model.RealmRole("till-supervisor"), model.RealmRole("base-employee")).
		composite(model.ClientRole("pos-app", "till-admin"), model.ClientRole("pos-app", "till-operator"))
}
