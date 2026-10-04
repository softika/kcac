#!/usr/bin/env bash
# Does Keycloak notice when somebody is removed from a group in the directory?
#
#   ./test/setup-ldap-federation.sh IMPORT      # or LDAP_ONLY
#   ./test/ldap-staleness-experiment.sh
#
# The answer decides whether an access review built on Keycloak data is still
# true, and it is the claim behind the "Next" item in ROADMAP.md. Reading the
# source suggests IMPORT copies membership at user creation and periodic sync
# does not revisit it. This measures rather than infers.
set -euo pipefail

KC="${KCAC_TEST_URL:-http://localhost:8080}"
REALM="${KCAC_TEST_REALM:-kcac-test}"
USER_UID="l.mover"
GROUP_CN="ldap-store-managers"
BASE_DN="dc=kcac,dc=test"

TOKEN="$(curl -fsS -X POST "$KC/realms/master/protocol/openid-connect/token" \
	-d grant_type=password -d client_id=admin-cli -d username=admin -d password=admin |
	python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')"
AUTH=(-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')

provider_mode() {
	curl -fsS "${AUTH[@]}" "$KC/admin/realms/$REALM/components?parent=$1&type=org.keycloak.storage.ldap.mappers.LDAPStorageMapper" |
		python3 -c 'import sys,json;print(next((m["config"]["mode"][0] for m in json.load(sys.stdin) if m["name"]=="ldap-groups"), "?"))'
}

PROVIDER_ID="$(curl -fsS "${AUTH[@]}" "$KC/admin/realms/$REALM/components?type=org.keycloak.storage.UserStorageProvider" |
	python3 -c 'import sys,json;print(next(c["id"] for c in json.load(sys.stdin) if c["name"]=="ldap-fixture"))')"
MODE="$(provider_mode "$PROVIDER_ID")"

user_id() {
	curl -fsS "${AUTH[@]}" "$KC/admin/realms/$REALM/users?username=$USER_UID&exact=true" |
		python3 -c 'import sys,json;d=json.load(sys.stdin);print(d[0]["id"] if d else "")'
}

kc_groups() {
	curl -fsS "${AUTH[@]}" "$KC/admin/realms/$REALM/users/$1/groups" |
		python3 -c 'import sys,json;g=[x["path"] for x in json.load(sys.stdin)];print("  keycloak says: "+(", ".join(g) if g else "(no groups)"))'
}

ldap_members() {
	docker exec kcac-test-ldap ldapsearch -x -LLL -H ldap://localhost \
		-b "cn=$GROUP_CN,ou=groups,$BASE_DN" -D "cn=admin,$BASE_DN" -w admin member 2>/dev/null |
		python3 -c '
import sys, re
ms = re.findall(r"uid=([^,]+)", sys.stdin.read())
print("  directory says: " + (", ".join(ms) if ms else "(no members)"))'
}

echo "group mapper mode under test: $MODE"
echo
UID_="$(user_id)"
[ -n "$UID_" ] || { echo "l.mover has not been imported; run setup-ldap-federation.sh first" >&2; exit 1; }

echo "--- 1. starting state ---"
ldap_members
kc_groups "$UID_"

echo
echo "--- 2. removing $USER_UID from $GROUP_CN in the directory ---"
docker exec -i kcac-test-ldap ldapmodify -x -H ldap://localhost -D "cn=admin,$BASE_DN" -w admin >/dev/null <<EOF
dn: cn=$GROUP_CN,ou=groups,$BASE_DN
changetype: modify
delete: member
member: uid=$USER_UID,ou=people,$BASE_DN
EOF
ldap_members

echo
echo "--- 3. before any sync, what does Keycloak report? ---"
kc_groups "$UID_"

echo
echo "--- 4. triggering a FULL sync ---"
curl -fsS -X POST "${AUTH[@]}" \
	"$KC/admin/realms/$REALM/user-storage/$PROVIDER_ID/sync?action=triggerFullSync" |
	python3 -c 'import sys,json;print("  "+json.dumps(json.load(sys.stdin)))'
kc_groups "$UID_"

echo
echo "--- 5. verdict ---"
STILL="$(curl -fsS "${AUTH[@]}" "$KC/admin/realms/$REALM/users/$UID_/groups" |
	python3 -c 'import sys,json;print("yes" if any("'"$GROUP_CN"'" in x["path"] for x in json.load(sys.stdin)) else "no")')"
if [ "$STILL" = "yes" ]; then
	echo "  Keycloak still reports the membership after a FULL sync."
	echo "  In mode $MODE, a directory removal does not reach Keycloak, so a review"
	echo "  built on Keycloak data over-reports this person's access."
else
	echo "  Keycloak dropped the membership in mode $MODE."
fi
