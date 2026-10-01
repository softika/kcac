# kcac

**Know who really has access to your Keycloak realm, and exactly why.**

[![Go](https://img.shields.io/badge/go-1.27-00ADD8)](https://go.dev)
[![Licence](https://img.shields.io/badge/licence-AGPL--3.0-blue)](LICENSE)
[![Read-only](https://img.shields.io/badge/keycloak-read--only-success)](#what-it-will-not-do)

A read-only CLI for Keycloak access reviews and audits. Keycloak will readily
tell you which roles were assigned to a user. Working out who *ends up with*
what is the hard part, and it is the part a review depends on.

## The problem, in one example

Meet `a.gruber`. In Keycloak she has **zero direct role assignments**. Not one.
If your access review is built from role assignments, she has nothing to review
and nobody ever looks at her account.

Here is what she can actually do:

```console
$ kcac explain a.gruber

a.gruber
  a.gruber@example.test · enabled · human

  4 entitlements

  base-employee                       realm role · 2 paths
    ├─ via group /Retail/StoreManagers (inherited from /Retail)
    └─ via group /Retail/StoreManagers → composite store-admin → till-supervisor → base-employee

  store-admin                         realm role
    └─ via group /Retail/StoreManagers

  pos-app:till-operator               client role
    └─ via group /Retail/StoreManagers → composite store-admin → pos-app:till-operator

  till-supervisor                     realm role
    └─ via group /Retail/StoreManagers → composite store-admin → till-supervisor
```

She belongs to exactly one group. Everything above follows from that:

* `/Retail/StoreManagers` grants `store-admin`
* its **parent** `/Retail` grants `base-employee`, and she is not a member of
  that parent
* `store-admin` contains `till-supervisor`, which in turn contains
  `base-employee`
* `store-admin` also reaches into a **different application** and grants
  `pos-app:till-operator`

**Zero direct assignments, four effective entitlements**, one of them in another
app. Note `base-employee` arrives two independent ways, so revoking one route
would leave the other in place.

Everybody in your realm who gets access this way is invisible to a role
assignment export. The export looks complete, so nobody checks. That is what
`kcac` is for.

Access reaches people through groups, parent groups, composite roles, composites
nested inside composites, and roles belonging to other clients. `kcac` resolves
all of those and reports the route for every grant.

## What can I use it for?

Questions `kcac` is built to answer:

* Who actually has access to this realm?
* Why does this person have this role?
* Which users inherit access through groups they are not direct members of?
* Which permissions arrive through composite roles?
* Who has access to another application indirectly?
* **Does removing one assignment actually remove the access?**
* Which accounts currently have no effective access at all?
* Can I show an auditor where an entitlement came from?

Typical uses:

**Access reviews.** Produce an inventory a manager can actually act on, because
each row says why the access exists.

**Security investigations.** Find every route by which one account holds a
particular entitlement.

**Privileged access reviews.** Surface indirect paths to sensitive roles, which
is exactly what checking direct assignments misses.

**Audit evidence.** A CSV plus a manifest recording what was collected and what
could not be determined.

## What you get

`kcac dump` writes one CSV row per account per effective entitlement. Abridged
to the column that matters, and wrapped here for readability:

```
username  entitlement      grant_path
--------  ---------------  ----------------------------------------------------------
m.huber   store-admin      direct assignment
a.gruber  base-employee    via group /Retail/StoreManagers (inherited from /Retail)
s.novak   till-supervisor  via group /Retail/StoreManagers/Region-East (inherited from
                           /Retail/StoreManagers) → composite store-admin → till-supervisor
n.nobody  (none)           (no access)
```

A list of usernames and roles tells you **what exists**. The grant path tells
you **why**, which is the difference between a spreadsheet and a review somebody
can sign off.

Accounts with no access get a row too. An account nobody looked at because it
happened to hold no roles is still a finding.

## Try it without touching your Keycloak

You should not point an unfamiliar tool at production, and you do not need to.
The repo ships a throwaway Keycloak with a realm built to exercise the cases
that make exports wrong:

```console
git clone https://github.com/softika/kcac && cd kcac
make kc-start     # Keycloak in Docker, seeded realm, waits until it is ready
make demo         # run kcac against it and see what it finds
make kc-stop      # tear it down
```

## Install

### Homebrew

```console
brew install softika/tap/kcac
```

Installs into a directory already on your `PATH`, so nothing to add and nothing
to reload.

### Install script

Linux and macOS, amd64 and arm64:

```console
curl -fsSL https://raw.githubusercontent.com/softika/kcac/main/install.sh | sh
```

It downloads one archive, verifies it against the published SHA-256 checksums,
and copies a single binary into the first directory that is both writable and
already on your `PATH`.

**It never asks for root, and never edits your shell config without asking
first.** On a stock macOS there is no directory that is both writable and on
`PATH`: `/usr/local/bin` is on `PATH` but owned by root, and `~/.local/bin` is
yours but not on `PATH`. When that happens it offers to fix it and waits:

```
  ~/.local/bin is not on your PATH, so 'kcac' will not be found yet.

  Add it to ~/.zshrc? [y/N]:
```

Say no and it prints the line to add yourself, or the `sudo` command to install
into `/usr/local/bin` instead. On Debian and Ubuntu it notices that `~/.profile`
already adds `~/.local/bin` and tells you to open a new shell rather than
editing anything.

Either way there is one step left, because **a startup file is only read when a
shell starts**. No installer can change the environment of the shell that
launched it. The last line of the output is the command to finish.

It knows zsh, bash and fish, will not add the same line twice, and **with no
terminal to ask on it does nothing**, so a CI job or an image build is never
edited behind its back or left hanging on a prompt.

Pin a version, pick a directory, or answer in advance:

```console
curl -fsSL .../install.sh | KCAC_VERSION=v0.1.2 KCAC_BINDIR=~/bin sh
curl -fsSL .../install.sh | KCAC_ADD_TO_PATH=1 sh
```

### Go

```console
go install github.com/softika/kcac/cmd/kcac@latest
```

### Download a binary

From [releases](https://github.com/softika/kcac/releases). Linux, macOS and
Windows, amd64 and arm64. Every archive is listed in `checksums.txt`, so you can
verify it yourself:

```console
sha256sum -c checksums.txt --ignore-missing
```

`kcac version` prints the exact commit it was built from, which is worth
including in a bug report.

### A note on macOS and code signing

These binaries are **not signed with an Apple Developer ID and not notarized**,
so macOS marks downloads as quarantined and refuses to run them: *"Apple could
not verify kcac is free of malware"*.

The Homebrew cask clears that flag on install, so `brew install` needs nothing
from you. For a binary you downloaded by hand:

```console
xattr -dr com.apple.quarantine "$(command -v kcac)"
```

That is Apple attesting nothing about this binary, not the binary being
untrustworthy, and it is worth knowing which. If you would rather not take
either on faith, build it yourself with `go install`, or check the SHA-256 in
the release against the one recorded in the cask.

### Uninstall

There is no uninstaller because there is nothing to uninstall. One binary, no
config file, no state directory, no background service:

```console
rm "$(command -v kcac)"
```

If the installer added `~/.local/bin` to your shell config, it marked the lines
`# Added by the kcac installer`. Remove them too, or keep them, since a `PATH`
entry pointing at a directory you still use is harmless.

Anything you generated with `-o` or `--manifest` is your data, and `kcac` never
had an opinion about it.

## Point it at your own realm

`kcac` only ever reads. It sends `GET` requests, plus the one `POST` needed to
log in, and there is no code path in it that writes to Keycloak.

**1. Create a client** in the realm you want to read, for example `kcac-audit`.
Set **Client authentication: On** and **Service accounts roles: On**, and turn
off every login flow (Standard, Direct access, Implicit).

**2. Give its service account read permissions.** From `realm-management`,
assign exactly these five and nothing else:

| Role | Used for |
|---|---|
| `view-users` | reading users and their role mappings |
| `view-clients` | reading applications and their roles |
| `view-realm` | reading realm roles and groups |
| `query-users` | paging through the user list |
| `query-groups` | paging through the group tree |

No write role is ever needed.

**3. Pass the secret** from the client's Credentials tab:

```console
export KCAC_CLIENT_SECRET='...'

kcac dump --url https://keycloak.example.com \
          --realm employees \
          --client-id kcac-audit \
          -o access.csv --manifest access.json
```

Use `KCAC_CLIENT_SECRET` or `--client-secret-file`. Avoid `--client-secret`,
because anyone on the machine can read it out of the process list. `kcac` warns
you if you do.

## Investigate one person

`dump` records the simplest explanation for each entitlement so the CSV stays
readable. When you need to argue about one account, `explain` shows every route:

```console
$ kcac explain d.dual

  base-employee                       realm role · 3 paths
    ├─ direct assignment of store-admin → composite till-supervisor → base-employee
    ├─ via group /Retail/StoreManagers (inherited from /Retail)
    └─ via group /Retail/StoreManagers → composite store-admin → till-supervisor → base-employee
```

Three independent routes to one entitlement. Revoke one and two remain. That
distinction decides whether a remediation ticket actually changes anything, and
it is the sort of thing an auditor asks about.

## Safe to run against production

The first run against a real realm should not be memorable. A 40,000 user realm
means more than 80,000 API calls, so `kcac`:

* limits requests in flight (`--concurrency`, default 8)
* backs off and retries on 429 and 5xx, respecting `Retry-After`
* times out each request on its own (`--timeout`)
* refreshes its token part way through, because a long run outlives one token
* always asks for an explicit page size instead of trusting server defaults

And it does not write to Keycloak.

## What it will not do

* **Write to Keycloak.** Not ever. There is no code that can.
* **Make up a last login.** Keycloak's event log is often switched off or kept
  only a few days. When `kcac` cannot tell, the column is empty and the reason
  is recorded. A confident "never logged in" that came from missing data is how
  the wrong person loses access.
* **Quietly guess account types.** The rules for labelling `service_account` and
  `federated` accounts are written down below, and `unknown` is a legitimate
  answer.
* **Run as a service.** No daemon, no database, no web interface, nothing to
  integrate. One command, one realm, one file. Feed that file to whatever you
  already use.
* **Change anything without telling you.** No config file, no state directory,
  and no edits to your shell config unless you answer yes when asked.

## Output reference

| Column | What it is |
|---|---|
| `user_id`, `username`, `email`, `enabled` | disabled accounts are reported, never hidden, because they still hold access |
| `entitlement`, `entitlement_type`, `client_id` | the role name, whether it is a realm or application role, and which application |
| **`grant_path`** | why this account holds this entitlement |
| `path_kind` | `direct`, `group`, `composite`, `group+composite`, or `none` |
| `path_count` | how many separate routes confer it; `explain` lists them all |
| `account_class` | `human`, `service_account`, `federated`, `unknown` |
| `last_login` | empty unless `--with-last-login`, and empty whenever unknown |

Rows are sorted by username, so you can diff this quarter against last quarter.

Files are written with owner-only permissions, because a `kcac` CSV is a
complete map of who can do what in your realm.

**Spreadsheet safety.** Keycloak permits usernames and role names beginning
`=`, `+`, `-` or `@`, and spreadsheets execute cells that start with those. CSV
quoting does not help, because it is stripped before the cell is parsed. `kcac`
puts an apostrophe in front of such values so a role name cannot run a formula
on the machine of whoever opens the review. It affects only cells that would
otherwise execute.

### Audit manifest

`--manifest access.json` writes a companion file recording the Keycloak version,
the counts, a SHA-256 of the CSV, and **everything the run could not
determine**:

```json
{
  "counts": { "users": 127, "grants": 143, "users_without_access": 1 },
  "output": { "bytes": 20428, "sha256": "f6bdbabd…" },
  "limitations": [
    "last_login is empty for every account: event logging is disabled on this realm"
  ]
}
```

CSV has no way to carry notes, and an auditor will ask what the gaps were. A
file that says what was found is half an answer; this is the other half.

## How do you know the result is correct?

Fair question for a tool whose only job is being correct.

Keycloak can calculate effective roles itself. It will not tell you *why*
someone has them, which is why that endpoint cannot replace `kcac`, but it makes
an excellent answer key. Every release checks `kcac`'s output against Keycloak's
own calculation **for every user in the seeded realm**, across realm roles and
application roles both.

That runs against Keycloak **22, 24 and 26** on every commit. Version 22 earns
its place because the group API changed in 23, so both code paths stay honest.

There are also 115 unit tests over the resolution logic: inherited group roles,
nested groups, composites inside composites, roles that loop back on themselves,
and entitlements reachable several ways at once. Coverage is above 90%.

## Why this is harder than exporting role mappings

If you have written an export like this yourself, these are the ones that bite.

**Parent group roles are inherited.** A member of
`/Retail/StoreManagers/Region-East` also holds everything granted on
`/Retail/StoreManagers` and on `/Retail`, without being listed as a member of
either. Keycloak does this in `RoleUtils.addGroupRoles`, which recurses
`getParent()`. Notably it is **not** the same as the `groups` claim in a token,
which is why it is so easy to miss.

**Composite roles nest without limit.** A role contains a role that contains a
role. Expanding one level under-reports access.

**Realm roles can contain client roles.** So walking only realm roles, or only
client role mappings, misses access in other applications.

**Page size defaults are not consistent.** `/users` returns 100 by default, but
`/groups/{id}/children` returns **10**. Omit `max` and every group with more
than ten children is silently truncated.

**The group and membership endpoints return a cut-down response by default.**
`briefRepresentation` defaults to `true` on `/groups` and `/users/{id}/groups`,
so fields go missing before you know to ask for them.

`kcac` handles all of these, and the seeded realm exists to prove it keeps
handling them.

## Found an incorrect result?

That is the most valuable report this project can get. If `kcac` gets something
wrong on your realm, `kcac fetch` writes out exactly what it read from the Admin
API, so the bug can be reproduced without anyone needing access to your systems.

**Read the file before attaching it.** It contains usernames, email addresses
and your group and role structure.

[Open an issue](https://github.com/softika/kcac/issues) with that file, your
Keycloak version, what `kcac` said, and what you expected.

## Roadmap

See [ROADMAP.md](ROADMAP.md), which also records what has been considered and
deliberately left out, with reasons.

If you run access reviews on Keycloak, these are the things worth telling me:

* What do you export today, and how do you review it?
* Which access relationships are hardest to explain to someone signing off?
* What do auditors actually ask you to prove?
* Do your applications authorize on **user attributes** rather than roles?
* Do you run **several realms**?

The last two decide what gets built next.

## Development

`make help` lists every target. The usual loop:

```console
make check              # formatting, vet and unit tests, same as CI
make kc-start           # seeded Keycloak on :8080
make test-integration   # integration and answer-key tests against it
make demo
make kc-stop

make kc-start-legacy    # Keycloak 22, to exercise the older group API
make test-all           # start Keycloak, run everything, stop it
make cover
make audit              # golangci-lint, govulncheck and a secret scan
make fixture            # rebuild the seeded realm
make dist               # release artefacts, exactly as a release builds them
```

CI runs the same targets, so local and CI cannot drift apart. Releases are built
with GoReleaser from a tag.

Integration runs pass `-count=1` for a reason: Go caches passing test results,
and nothing it hashes changes when the Keycloak container behind
`localhost:8080` changes version, so without it a run against Keycloak 22
cheerfully reports the cached result from Keycloak 26.

Contributions welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for what gets
merged and what does not.

## Licence

AGPL-3.0. See [LICENSE](LICENSE).

**Running `kcac` inside your company creates no obligations at all.** Run it,
read it, keep the output private. The licence only matters if you modify `kcac`
and then offer your modified version to other people as a hosted service.

`kcac` is not affiliated with or endorsed by Red Hat. "Keycloak" is their
trademark, used here only to say what this tool talks to.
