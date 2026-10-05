# AGENTS.md

## Project overview and scope

`kcac` is a read-only Go CLI for Keycloak access reviews. It collects one realm,
resolves effective entitlements and their grant paths, and exports audit evidence.
Commands are `dump` (CSV), `explain` (all grant paths for a user), `fetch` (raw JSON
snapshot), and `version`.

- Read `CONTRIBUTING.md` before changing behavior; consult `README.md` for the
  public CLI/output contract and `ROADMAP.md` for scope decisions.
- Never add a production code path that modifies Keycloak. Only GET requests and
  the OAuth token POST are allowed. Test-fixture provisioning is separate.
- Do not add a daemon, database, persistent application state, web UI, or outbound
  integrations. New flags, output formats, and CSV column changes require prior
  discussion with the maintainer. If a task needs one of these, stop and say so
  rather than implementing it.
- These instructions apply repository-wide. A nested `AGENTS.md` takes precedence
  for files in its directory tree.

## Repository map

- `cmd/kcac/`: command dispatch, standard-library `flag` parsing, and CLI I/O.
- `internal/kc/`: Admin API client, authentication, pagination, collection, groups,
  and login events.
- `internal/model/`: Keycloak representations and role identities.
- `internal/resolve/`: pure, I/O-free effective-access and grant-path resolution.
- `internal/output/`: CSV, explanations, account classification, and audit manifests.
- `test/gen/main.go`: source of the generated `testdata/realm-export.json` fixture.
- `test/`: Docker Compose, readiness scripts, and LDAP federation experiments.
- `.github/workflows/`: authoritative CI checks and Keycloak version matrix.

## Setup and commands

Run commands from the repository root. Use the Go version required by `go.mod`;
do not lower it to accommodate an older local toolchain. Docker with Compose is
needed for integration tests, not unit tests.

| Command | Purpose |
| --- | --- |
| `make build` | Build `./kcac` with version metadata. |
| `make check` | Check formatting, vet normal/integration builds, and run race-enabled unit tests. |
| `make fmt` | Format Go files; modifies files in `cmd`, `internal`, and `test`. |
| `make test` | Uncached, race-enabled unit tests; no live Keycloak required. |
| `go test -count=1 -race ./internal/resolve -run TestName` | Run a focused test; replace package/name as needed. |
| `make lint` | Run the pinned golangci-lint version with `.golangci.yml`. |
| `make kc-start KC_VERSION=26.0` | Start the seeded local Keycloak and wait for readiness. |
| `make kc-start-legacy` | Start Keycloak `22.0`, which exercises the pre-23 group fallback. |
| `make test-integration` | Run integration and oracle tests against the running fixture. |
| `make cover` | Measure internal-package coverage; requires the running fixture and overwrites `coverage.out`. |
| `make fixture` | Regenerate the realm export from `test/gen/main.go`. |
| `make ldap-start` | Start Keycloak plus a seeded OpenLDAP and federate them; `LDAP_GROUP_MODE` and `LDAP_EDIT_MODE` select the mapper and edit modes. |
| `make ldap-experiment` | Measure whether a directory removal reaches Keycloak; requires `make ldap-start`. |
| `make tidy` | Tidy and verify module dependencies; may modify `go.mod` and `go.sum`. |
| `make audit` | Run lint, vulnerability checks, and a Docker-based secret scan. |
| `make help` | List all available targets. |

`make kc-stop` removes the local Keycloak and LDAP fixture containers **and their
data volumes**. Confirm before discarding existing fixture data. To switch
Keycloak versions, run `make kc-stop` and then `make kc-start KC_VERSION=...`.
`make test-all` starts the fixture, runs unit/integration tests, and stops it, but
a failed prerequisite can leave it running. Do not run lifecycle targets
concurrently or with `make -j`.

## Testing and correctness

- For code changes, add or update tests alongside the affected package. Reproduce
  bugs with a failing test before fixing them, then run focused tests and the
  relevant broader checks. Do not weaken a correct test to make it pass.
- Run `make check` and `make lint` for Go changes. Report actual commands/results
  and any checks skipped because the toolchain, Docker, or network is unavailable.
  Documentation-only changes need link/path/command checks, not a live realm.
- Access-resolution changes need unit coverage and agreement with Keycloak's
  effective-role answer key in `internal/resolve/oracle_integration_test.go`.
  Use `internal/resolve/builder_test.go` for in-memory realm construction patterns.
- Verify API and resolution changes against the CI matrix: Keycloak `22.0`,
  `24.0`, and `26.0`, using a fresh seeded fixture for each version. Version 22
  exercises the pre-23 nested-`subGroups` fallback rather than `/children`.
- Integration tests use the `integration` build tag. Keep `-count=1`: Go's test
  cache cannot distinguish Keycloak versions behind the same server URL.
- Change seeded cases in `test/gen/main.go`, explain which failure each guards
  against, and run `make fixture`. Commit the regenerated export with its source.
  Restart the disposable fixture to load it. CI checks that regeneration is clean.
- Preserve parent-group inheritance, nested/cyclic composites, cross-client roles,
  multiple independent grant paths, disabled accounts, and accounts with no access.
  Keep `dump`'s simplest-path summary distinct from `explain`'s all-path view.

## Code conventions

- Follow adjacent code and existing helpers; keep files and functions focused.
  Prefer existing standard-library patterns over new dependencies or frameworks.
- Keep resolution pure; HTTP belongs in `internal/kc`, presentation in
  `internal/output`, and command orchestration in `cmd/kcac`.
- Use `gofmt` and the repository's linter configuration. Comments should explain
  why, especially surprising Keycloak behavior, rather than narrate the code.
- Return actionable errors with context; preserve error chains with `%w` and use
  `errors.Is`/`errors.As` where appropriate. Do not silently discard failures.
- Propagate caller contexts to outbound requests, close response bodies, and
  preserve cancellation, bounded concurrency, per-request timeouts, token refresh,
  and retry/backoff behavior. Do not increase server load for a speed improvement.
- Explicitly paginate API endpoints; never rely on server page-size defaults.
  Preserve required `briefRepresentation` handling and legacy group compatibility.
- Preserve deterministic output, CSV column compatibility, formula-injection
  protection, and owner-only output file permissions. Unknown login/account data
  must remain explicitly unknown, not be guessed or silently presented as absent.

## Security and data handling

- Never commit real credentials, access tokens, customer snapshots, or audit CSVs.
  Snapshots contain usernames, emails, group structure, and permissions; sanitize
  reproductions before sharing. Do not copy local output files into fixtures.
- Prefer `KCAC_CLIENT_SECRET` or `--client-secret-file`; command-line secrets are
  visible in process listings. Never log secrets or token responses.
- Public throwaway fixture credentials are intentional; never reuse them outside
  the disposable test environment. Do not point tests or LDAP setup scripts at
  production or another non-disposable realm.
- Preserve least-privilege operation: `view-users`, `view-clients`, `view-realm`,
  `query-users`, and `query-groups`; never require write roles.
- Report security vulnerabilities privately via GitHub's Security tab, as described
  in `CONTRIBUTING.md`, rather than through public issues.

## Commits and pull requests

- Keep changes scoped; do not include generated binaries, coverage reports, local
  exports, or unrelated formatting changes. Update user-facing docs when changing
  documented behavior.
- Commit or push only when asked. Use `<type>: <description>` commit subjects
  (`feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`); CI does not
  enforce this, so check it yourself. Contributions require DCO sign-off (`git commit -s`); use the contributor's
  configured identity, never invent one. Read the licensing terms in
  `CONTRIBUTING.md` before submitting.
- Review the full branch diff before a PR. Include a concise summary, test results,
  affected Keycloak versions, and any compatibility or security implications.
