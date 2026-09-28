#!/usr/bin/env bash
# Waits until the seeded realm is importable and serving.
#
# Polls the realm's OIDC discovery document rather than a health endpoint,
# because that is what actually proves the import finished.
set -euo pipefail

URL="${KCAC_TEST_URL:-http://localhost:8080}"
REALM="${KCAC_TEST_REALM:-kcac-test}"
TIMEOUT="${TIMEOUT:-120}"

printf 'waiting for %s/realms/%s ' "$URL" "$REALM"
for ((i = 0; i < TIMEOUT; i++)); do
  if curl -fsS "$URL/realms/$REALM/.well-known/openid-configuration" >/dev/null 2>&1; then
    printf ' ready\n'
    exit 0
  fi
  printf '.'
  sleep 1
done

printf ' TIMED OUT after %ss\n' "$TIMEOUT" >&2
echo "container logs:" >&2
docker logs --tail 40 kcac-test-keycloak >&2 || true
exit 1
