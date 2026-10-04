#!/usr/bin/env bash
# Wire the seeded LDAP directory into the seeded Keycloak realm.
#
#   ./test/setup-ldap-federation.sh [GROUP_MAPPER_MODE] [PROVIDER_EDIT_MODE]
#
# GROUP_MAPPER_MODE defaults to IMPORT, the mode worth investigating: it copies
# group membership into Keycloak's own tables, and the question this fixture
# exists to answer is whether anything refreshes that afterwards.
# Use LDAP_ONLY to compare against membership read live from the directory.
#
# Safe to re-run; it removes a previous provider of the same name first.
set -euo pipefail

KC="${KCAC_TEST_URL:-http://localhost:8080}"
REALM="${KCAC_TEST_REALM:-kcac-test}"
GROUP_MODE="${1:-IMPORT}"
EDIT_MODE="${2:-READ_ONLY}"

# Reachable from inside the Keycloak container, not from the host.
LDAP_URL="ldap://openldap:389"
BASE_DN="dc=kcac,dc=test"

say() { printf '%s\n' "$*"; }

token() {
	curl -fsS -X POST "$KC/realms/master/protocol/openid-connect/token" \
		-d grant_type=password -d client_id=admin-cli \
		-d username=admin -d password=admin |
		python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])'
}

# slapd accepts connections a moment after the container starts, and Keycloak
# cannot create a provider pointing at a directory that is not answering yet.
say "=== waiting for the directory ==="
for _ in $(seq 1 60); do
	if docker exec kcac-test-ldap ldapsearch -x -H ldap://localhost \
		-b "$BASE_DN" -D "cn=admin,$BASE_DN" -w admin >/dev/null 2>&1; then
		say "  ready"
		break
	fi
	sleep 1
done

TOKEN="$(token)"
AUTH=(-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')

say "=== removing any previous provider ==="
existing="$(curl -fsS "${AUTH[@]}" "$KC/admin/realms/$REALM/components?type=org.keycloak.storage.UserStorageProvider" |
	python3 -c 'import sys,json;print(next((c["id"] for c in json.load(sys.stdin) if c["name"]=="ldap-fixture"), ""))')"
if [ -n "$existing" ]; then
	curl -fsS -X DELETE "${AUTH[@]}" "$KC/admin/realms/$REALM/components/$existing" && say "  deleted $existing"
else
	say "  none"
fi

say "=== creating LDAP provider (editMode=$EDIT_MODE) ==="
curl -fsS -X POST "${AUTH[@]}" "$KC/admin/realms/$REALM/components" -d "{
  \"name\": \"ldap-fixture\",
  \"providerId\": \"ldap\",
  \"providerType\": \"org.keycloak.storage.UserStorageProvider\",
  \"config\": {
    \"enabled\": [\"true\"],
    \"priority\": [\"0\"],
    \"vendor\": [\"other\"],
    \"connectionUrl\": [\"$LDAP_URL\"],
    \"bindDn\": [\"cn=admin,$BASE_DN\"],
    \"bindCredential\": [\"admin\"],
    \"usersDn\": [\"ou=people,$BASE_DN\"],
    \"usernameLDAPAttribute\": [\"uid\"],
    \"rdnLDAPAttribute\": [\"uid\"],
    \"uuidLDAPAttribute\": [\"entryUUID\"],
    \"userObjectClasses\": [\"inetOrgPerson\"],
    \"searchScope\": [\"1\"],
    \"editMode\": [\"$EDIT_MODE\"],
    \"importEnabled\": [\"true\"],
    \"syncRegistrations\": [\"false\"],
    \"pagination\": [\"true\"]
  }
}" >/dev/null

PROVIDER_ID="$(curl -fsS "${AUTH[@]}" "$KC/admin/realms/$REALM/components?type=org.keycloak.storage.UserStorageProvider" |
	python3 -c 'import sys,json;print(next(c["id"] for c in json.load(sys.stdin) if c["name"]=="ldap-fixture"))')"
say "  provider id: $PROVIDER_ID"

say "=== creating group mapper (mode=$GROUP_MODE) ==="
curl -fsS -X POST "${AUTH[@]}" "$KC/admin/realms/$REALM/components" -d "{
  \"name\": \"ldap-groups\",
  \"providerId\": \"group-ldap-mapper\",
  \"providerType\": \"org.keycloak.storage.ldap.mappers.LDAPStorageMapper\",
  \"parentId\": \"$PROVIDER_ID\",
  \"config\": {
    \"groups.dn\": [\"ou=groups,$BASE_DN\"],
    \"group.name.ldap.attribute\": [\"cn\"],
    \"group.object.classes\": [\"groupOfNames\"],
    \"membership.ldap.attribute\": [\"member\"],
    \"membership.attribute.type\": [\"DN\"],
    \"membership.user.ldap.attribute\": [\"uid\"],
    \"preserve.group.inheritance\": [\"false\"],
    \"mode\": [\"$GROUP_MODE\"],
    \"user.roles.retrieve.strategy\": [\"LOAD_GROUPS_BY_MEMBER_ATTRIBUTE\"],
    \"drop.non.existing.groups.during.sync\": [\"false\"]
  }
}" >/dev/null
say "  created"

say "=== triggering a full sync ==="
curl -fsS -X POST "${AUTH[@]}" \
	"$KC/admin/realms/$REALM/user-storage/$PROVIDER_ID/sync?action=triggerFullSync" |
	python3 -c 'import sys,json;d=json.load(sys.stdin);print("  "+json.dumps(d))' || say "  (no body)"

say "=== federated users Keycloak now knows about ==="
curl -fsS "${AUTH[@]}" "$KC/admin/realms/$REALM/users?briefRepresentation=false&max=200" |
	python3 -c '
import sys, json
for u in json.load(sys.stdin):
    if u.get("federationLink"):
        print("  %s  id=%s" % (u["username"], u["id"]))
'

say ""
say "  Provider id for later calls: $PROVIDER_ID"
