# Roadmap

`kcac` is deliberately small. This is the whole plan, not a teaser.

## v0.1: effective access, correctly

- [x] Read-only Admin API client: token refresh, bounded concurrency, backoff,
      `Retry-After`, per-request timeouts
- [x] Pagination with an explicit `max` on every endpoint
- [x] Group-children capability detection with a pre-Keycloak-23 fallback
- [x] Realm collector producing a raw snapshot (`kcac fetch`)
- [x] Seeded test realm covering the documented edge cases
- [x] Effective access resolution: ancestor group inheritance, recursive
      composite expansion across the realm/client boundary, cycle safety
- [x] `kcac dump`, a CSV with `grant_path` and `path_kind`
- [x] `kcac explain <user>`, every path to every entitlement, as a tree
- [x] Run manifest: versions, counts, and every limitation that applied
- [x] CI across Keycloak majors, including a pre-23 server
- [x] Install script with checksum verification, and a Homebrew tap at
      `softika/homebrew-tap`

Remaining before tagging v0.1.0: a release build and the `v0.1.0` tag itself.

## Not planned

Scheduling, a database, a web UI, multi-realm campaigns, reviewer workflows,
sign-off capture, and pushing to GRC platforms are all out of scope. `kcac` reads
one realm and writes one file. If you need a review workflow, your compliance
platform already has one, and `kcac` exists to give it correct input.

## Next

**Report LDAP federation modes beside federated grants.** This one is not
speculative: it affects group memberships, which `kcac` already reports today.

A group LDAP mapper has its own `mode`, separate from the provider's Edit Mode.
Rather than infer the difference, there is now an LDAP fixture in the repo that
measures it. Remove somebody from a group in the directory, then ask Keycloak:

```console
make ldap-start LDAP_GROUP_MODE=IMPORT
make ldap-experiment
```

**`IMPORT`: the removal never arrives.**

```
directory says: l.stable
keycloak says:  /ldap-store-managers
triggering a FULL sync... {"updated": 2, "status": "2 updated users"}
keycloak says:  /ldap-store-managers
```

The sync reports two users updated and the stale membership survives it.
Somebody removed from a group in the directory keeps the Keycloak membership
indefinitely, so Keycloak over-reports their access and `kcac` faithfully
reports Keycloak.

**`LDAP_ONLY`: the removal arrives, but caching can delay it.** Membership is
read from the directory, so it is correct. However the provider's default
`cachePolicy` caches it, and in testing Keycloak kept reporting the old
membership until something invalidated that cache. With `cachePolicy=NO_CACHE`
the removal was visible immediately.

So there are two distinct ways a review can be reading access that no longer
exists at the source, and neither shows up in the output. That is worse than a
revoke failing, because it over-reports rather than under-reports.

`kcac` already knows which accounts are federated, so reading each provider's
Edit Mode, each group mapper's `mode` and the cache policy, recording them in
the manifest, and flagging the affected grants is cheap and is something nothing
else does.

## Considered and deliberately not built

**Reading several realms from one client.** A service account in the `master`
realm can be granted the same five read-only roles per realm, and
`GET /admin/realms` usefully returns only the realms that token may read. So
`kcac --all-realms` would work, and it needs no extra privilege.

It is not built because "all" would quietly mean "all the realms somebody
remembered to grant". A realm created next month would be missing from the
review with nothing to indicate it, and a report that silently under-covers is
the exact problem `kcac` exists to prevent.

If you run several realms and would use this, please open an issue and say how
you would want the missing-realm problem handled. That is the part that needs
solving, not the code.

**Access granted through user attributes.** Plenty of realms do not authorize on
Keycloak roles at all. They store something like `app_groups` or `entitlements`
as a user attribute, often synced in from an external directory, and the
application reads that attribute to decide what someone can do.

`kcac` misses this completely today. Someone with no roles and no groups can hold
real access through an attribute, and a role export shows them as having nothing.
That is the same gap as inherited group roles, one step further out, so it
belongs here in principle.

Three things have to be solved before it would be worth having:

* **Which attributes mean access?** A role says what it is. An attribute is just a
  string. `locale` is noise and `app_groups` is access, and only you know which is
  which, so it would have to be named explicitly rather than guessed.
* **Attributes hold personal data.** This output is already a complete map of who
  can do what. Adding every attribute could put employee numbers or phone numbers
  in it too. "All attributes" can never be the default.
* **They may not be readable at all.** Since Keycloak 24 the declarative user
  profile is on by default and unmanaged attributes are disabled, so attributes
  can be silently absent from the Admin API. Reporting an empty column would tell
  a reviewer "no attribute access here" when the truth is "cannot see". `kcac`
  would have to detect that and say so.

There is one thing worth doing here that nothing else does, and a reader on
r/keycloak corrected my original understanding of it. Whether a revoke sticks
depends on the LDAP provider's **Edit Mode**, and the three modes fail
differently:

* `READ_ONLY`: Keycloak rejects the edit with an error, so the revoke fails
  loudly and nobody believes it worked.
* `WRITABLE`: the change is written back to the directory, so it holds.
* `UNSYNCED`: the change is stored in Keycloak's local database only, and the
  directory keeps the old value. This is the mode where a revoke looks done and
  is not done anywhere that matters.

There is a further trap on the same page: the initial mappers are configured
from the Edit Mode chosen when the provider was **created**, and changing the
mode afterwards does not reconfigure them. A provider created as `UNSYNCED` and
switched later can still be reading from the local database.

So the useful thing is to record each provider's Edit Mode beside any grant that
comes from a federated account.

**If your applications authorize on user attributes rather than roles, please open
an issue.** Knowing which attributes you use and where they come from is what
would shape this.

## Possible later

- Account classification beyond the current heuristics (they are documented in
  the README and `unknown` is a legitimate answer)
- Diffing two snapshots to show what access changed between reviews
