# Roadmap

`kcac` is deliberately small. This is the whole plan, not a teaser.

## v0.1 — effective access, correctly

- [x] Read-only Admin API client: token refresh, bounded concurrency, backoff,
      `Retry-After`, per-request timeouts
- [x] Pagination with an explicit `max` on every endpoint
- [x] Group-children capability detection with a pre-Keycloak-23 fallback
- [x] Realm collector producing a raw snapshot (`kcac fetch`)
- [x] Seeded test realm covering the documented edge cases
- [x] Effective access resolution: ancestor group inheritance, recursive
      composite expansion across the realm/client boundary, cycle safety
- [x] `kcac dump` — CSV with `grant_path` and `path_kind`
- [x] `kcac explain <user>` — every path to every entitlement, as a tree
- [x] Run manifest: versions, counts, and every limitation that applied
- [x] CI across Keycloak majors, including a pre-23 server

Remaining before tagging v0.1.0: a release build and the `v0.1.0` tag itself.

## Not planned

Scheduling, a database, a web UI, multi-realm campaigns, reviewer workflows,
sign-off capture, and pushing to GRC platforms are all out of scope. `kcac` reads
one realm and writes one file. If you need a review workflow, your compliance
platform already has one — `kcac` exists to give it correct input.

## Soon

- Homebrew tap. The cask is already generated on every release build; it just
  needs the `softika/homebrew-tap` repository and a token that can push to it.

## Possible later

- Account classification beyond the current heuristics (they are documented in
  the README and `unknown` is a legitimate answer)
- Diffing two snapshots to show what access changed between reviews
