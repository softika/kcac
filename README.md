# kcac

**Find out who really has access to your Keycloak realm, and why.**

[![Go](https://img.shields.io/badge/go-1.27-00ADD8)](https://go.dev)
[![Licence](https://img.shields.io/badge/licence-AGPL--3.0-blue)](LICENSE)
[![Read-only](https://img.shields.io/badge/keycloak-read--only-success)](#what-it-will-not-do)

Keycloak can tell you who you *assigned* a role to. It is much harder to find out
everyone who actually *ends up with* that role. People inherit access through
groups, through parent groups, and through roles that quietly contain other roles.

`kcac` works that out for you, and tells you where every piece of access came from.

## The problem, in one example

Meet `a.gruber`. In Keycloak, **she has no role assignments at all.** Not one. If
your access review is built from role assignments, she has nothing to review and
nobody ever looks at her account.

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

(Connection flags are left out above to keep it readable. The real command needs
`--url`, `--realm` and `--client-id`, shown in full further down.)

She belongs to exactly one group. Everything above follows from that:

* her group grants `store-admin`
* the **parent** of her group grants `base-employee`, and she is not a member of
  that parent
* `store-admin` contains `till-supervisor`, which in turn contains `base-employee`
* `store-admin` also reaches into a **different application** and grants
  `till-operator` there

Four pieces of access, one of them in another app, from a single group membership
and zero role assignments. Everybody in your realm who gets access this way is
invisible to a role assignment export. The export looks complete, so nobody checks.

That is what `kcac` is for.

## What you get

One CSV row per person per piece of access, with a plain English reason attached:

```
username  entitlement      grant_path
--------  ---------------  ---------------------------------------------------------
m.huber   store-admin      direct assignment
a.gruber  base-employee    via group /Retail/StoreManagers (inherited from /Retail)
s.novak   till-supervisor  via group /Retail/StoreManagers/Region-East (inherited from
                           /Retail/StoreManagers) → composite store-admin → till-supervisor
n.nobody  (none)           (no access)
```

That last column is the whole point. A list of names and roles is just a database
query. The reason is what lets a manager actually decide "yes, keep it" or "no,
take it away". It is also what turns a spreadsheet into audit evidence.

Note `n.nobody`. Accounts with no access still get a row, because an account
nobody has looked at is a finding in its own right.

## Try it in two minutes

You do not need your own Keycloak, and you should not point an unfamiliar tool at
production anyway. The repo ships a throwaway one:

```console
git clone https://github.com/softika/kcac && cd kcac
make kc-start     # Keycloak in Docker, seeded with a deliberately tricky realm
make demo         # run kcac against it and see what it finds
make kc-stop      # tear it down
```

The seeded realm has groups nested three deep with roles at every level, roles
that contain other roles, roles that reach across into another application, and a
user who holds the same role by three different routes. All the cases that are
easy to get wrong, in one place.

## Install

**Script** (Linux and macOS, amd64 and arm64):

```console
curl -fsSL https://raw.githubusercontent.com/softika/kcac/main/install.sh | sh
```

It downloads one archive, checks it against the published SHA-256 checksums, and
copies a single binary into the first directory that is both writable and already
on your `PATH`, trying `/usr/local/bin`, then `~/.local/bin`, then `~/bin`.

**It never asks for root, and never edits your shell config without asking
first.** On a stock macOS, and on plenty of Linux setups, there is no directory
that is both writable and on `PATH`: `/usr/local/bin` is on `PATH` but owned by
root, and `~/.local/bin` is yours but not on `PATH`. When that happens the
installer offers to fix it and waits for an answer:

```
  ~/.local/bin is not on your PATH, so 'kcac' will not be found yet.

  Add it to ~/.zshrc? [y/N]:
```

Say no and it prints the line for you to add yourself, or the command to install
into `/usr/local/bin` with `sudo` instead. Say yes and it appends two lines, one
of them a comment saying it put them there.

Either way there is one step left, because **a startup file is only read when a
shell starts**, so the shell you ran the installer in still cannot see `kcac`.
No installer can change the environment of the shell that launched it. The last
line of the output is the `source` command to finish, or just open a new
terminal.

It knows zsh, bash and fish, it will not add the same line twice, and **with no
terminal to ask on it does nothing**, so a CI job or an image build is never
edited behind its back or left hanging on a prompt.

To answer in advance, for scripted installs:

```console
curl -fsSL .../install.sh | KCAC_ADD_TO_PATH=1 sh   # yes, add it
curl -fsSL .../install.sh | KCAC_ADD_TO_PATH=0 sh   # no, just tell me
```

Read the script first if you would rather, it is short:

```console
curl -fsSL https://raw.githubusercontent.com/softika/kcac/main/install.sh | less
```

Pin a version or pick a directory with `KCAC_VERSION` and `KCAC_BINDIR`:

```console
curl -fsSL .../install.sh | KCAC_VERSION=v0.1.1 KCAC_BINDIR=~/bin sh
```

**Homebrew** (installs into a directory already on your `PATH`, so nothing to
add and nothing to reload):

```console
brew install softika/tap/kcac
```

A note on macOS, since it concerns trust. These binaries are **not signed with
an Apple Developer ID and not notarized**, so macOS marks the download as
quarantined and refuses to run it: *"Apple could not verify kcac is free of
malware"*. The cask clears that flag on the binary it just installed, which is
what you would otherwise type yourself:

```console
xattr -dr com.apple.quarantine "$(command -v kcac)"
```

That is Apple attesting nothing about this binary, not the binary being
untrustworthy, and it is worth knowing which. If you would rather not take
either on faith, build it yourself with `go install`, or check the SHA-256 in
the release's `checksums.txt` against the one recorded in the cask.

**Go:**

```console
go install github.com/softika/kcac/cmd/kcac@latest
```

**Manually:** grab an archive from [releases](https://github.com/softika/kcac/releases).
Linux, macOS and Windows, amd64 and arm64. Every archive is listed in
`checksums.txt` on the release, so you can verify it yourself:

```console
sha256sum -c checksums.txt --ignore-missing
```

`kcac version` prints the exact commit it was built from, which is worth
including in a bug report.

### Uninstall

There is no uninstaller because there is nothing to uninstall. `kcac` is one
binary with no config file, no state directory, no background service, and it
does not touch your shell configuration. Deleting it is the whole job:

```console
rm "$(command -v kcac)"
```

If you never added it to your `PATH`, it is wherever the installer said it put
it, usually:

```console
rm ~/.local/bin/kcac
```

If you let the installer put `~/.local/bin` on your `PATH`, it left two lines in
your shell config marked `# Added by the kcac installer`. Delete them too, or
keep them, since a `PATH` entry pointing at a directory you still use is
harmless either way.

Anything you asked for with `-o` or `--manifest` is your data, and `kcac` never
had an opinion about it.

## Point it at your own realm

`kcac` only ever reads. It sends `GET` requests, plus the one `POST` needed to log
in, and there is no code path in it that writes to Keycloak.

Create a client in the realm you want to look at:

1. **Clients → Create client**, client ID `kcac-audit`
2. **Client authentication: On** and **Service accounts roles: On**. Turn every
   login flow off (Standard, Direct access, Implicit).
3. **Service accounts roles → Assign role → Filter by clients**, then assign
   exactly these five from `realm-management` and nothing else:

   | Role | Why it is needed |
   |---|---|
   | `view-users` | read users and their roles |
   | `view-clients` | read applications and their roles |
   | `view-realm` | read realm roles and groups |
   | `query-users` | page through the user list |
   | `query-groups` | page through the group tree |

4. Copy the secret from the **Credentials** tab.

```console
export KCAC_CLIENT_SECRET='...'

kcac dump --url https://keycloak.example.com \
          --realm employees \
          --client-id kcac-audit \
          -o access.csv --manifest access.json
```

Use `KCAC_CLIENT_SECRET` or `--client-secret-file`. Avoid `--client-secret`,
because anyone on the machine can read it out of the process list. `kcac` warns
you if you do it anyway.

## Auditing one person

`dump` gives you the simplest reason for each piece of access, so the CSV stays
readable. When you need to argue about one specific account, `explain` shows every
route at once:

```console
$ kcac explain d.dual

  base-employee                       realm role · 3 paths
    ├─ direct assignment of store-admin → composite till-supervisor → base-employee
    ├─ via group /Retail/StoreManagers (inherited from /Retail)
    └─ via group /Retail/StoreManagers → composite store-admin → till-supervisor → base-employee
```

Revoking one of those three routes leaves the other two in place. That is exactly
the kind of thing that gets missed, and exactly what an auditor asks about.

## Safe to run against production

The first time you run this against a real realm, it should not be memorable. A
40,000 user realm means more than 80,000 API calls, so `kcac`:

* limits how many requests are in flight at once (`--concurrency`, default 8)
* backs off and retries on 429 and 5xx, and respects `Retry-After`
* times out each request on its own (`--timeout`)
* refreshes its token part way through, because a long run outlives one token
* always asks for an explicit page size instead of trusting server defaults

## What it will not do

* **Write to Keycloak.** Not ever. There is no code that can.
* **Make up a last login.** Keycloak's event log is often switched off or kept
  only for a few days. When `kcac` cannot tell, the column is empty and the
  reason is written down. A confident "never logged in" that came from missing
  data is how the wrong person loses access.
* **Quietly guess account types.** The rules for labelling service accounts and
  directory accounts are written down below, and `unknown` is a valid answer.
* **Run as a service.** No daemon, no database, no web interface, nothing to
  integrate. One command, one realm, one file. Feed that file to whatever you
  already use.
* **Change anything without telling you.** No config file, no state directory,
  and no edits to your shell config unless you answer yes when asked.
  Uninstalling is deleting one binary.

## Output reference

| Column | What it is |
|---|---|
| `user_id`, `username`, `email`, `enabled` | disabled accounts are reported, never hidden, because they still hold access |
| `entitlement`, `entitlement_type`, `client_id` | the role name, whether it is a realm or application role, and which application |
| **`grant_path`** | why this person has this access |
| `path_kind` | `direct`, `group`, `composite`, `group+composite` or `none` |
| `path_count` | how many separate routes lead here; `explain` lists them all |
| `account_class` | `human`, `service_account`, `federated` or `unknown` |
| `last_login` | empty unless you pass `--with-last-login`, and empty whenever unknown |

Rows are sorted by username, so you can diff this quarter against last quarter.

Files are written with owner-only permissions, because a `kcac` CSV is a complete
map of who can do what in your realm.

One deliberate alteration: if a name begins with `=`, `+`, `-` or `@`, an
apostrophe is put in front of it. Spreadsheets execute cells starting with those
characters, and Keycloak allows them in usernames and role names, so opening an
unsanitised review file could run someone else's formula on your machine. It
affects only cells that would otherwise execute, which are worth a second look
anyway.

`--manifest access.json` writes a companion file next to the CSV. It records the
Keycloak version, the counts, a SHA-256 of the CSV, and **everything the run could
not determine**:

```json
{
  "counts": { "users": 127, "grants": 143, "users_without_access": 1 },
  "output": { "bytes": 20428, "sha256": "f6bdbabd…" },
  "limitations": [
    "last_login is empty for every account: event logging is disabled on this realm"
  ]
}
```

CSV has no way to carry notes, and an auditor will ask what the gaps were. Now you
have an answer.

## How do you know the numbers are right?

Fair question for a tool whose whole job is being correct.

Keycloak can calculate effective roles itself. It will not tell you *why* someone
has them, which is why that endpoint cannot replace `kcac`, but it is a perfect
answer key. Every release checks `kcac`'s output against Keycloak's own
calculation, **for every user in the test realm**, covering both realm roles and
application roles.

That runs against Keycloak 22, 24 and 26 on every commit, and all three are
verified today. Version 22 earns its place because the group API changed in 23, so
both code paths stay honest.

There are also 99 unit tests covering the tricky parts: inherited group roles,
nested roles, roles that loop back on themselves, and people reachable by several
routes at once. Coverage sits above 90%.

## A few details that are easy to get wrong

If you have written an export like this yourself, these are the ones that bite:

* Group roles are inherited **downward from parent groups**. Someone in
  `/Retail/StoreManagers/Region-East` also holds everything granted on
  `/Retail/StoreManagers` and on `/Retail`. This is not the same as the `groups`
  claim in a token, which is why it gets missed.
* Roles containing roles nest without limit, and a realm role can contain an
  application role, so walking only realm roles under-reports access.
* The group and membership endpoints return a cut down response by default, so
  fields go missing before you know to ask for them.
* Page size defaults are not consistent. `/users` returns 100 by default, but
  subgroups return **10**. Any group with more than ten children gets silently
  cut short.

`kcac` handles all of these, and the seeded realm exists to prove it keeps
handling them.

## Licence

AGPL-3.0. See [LICENSE](LICENSE).

**Running `kcac` inside your company creates no obligations at all.** Run it, read
it, keep the output private. The licence only matters if you modify `kcac` and
then offer your modified version to other people as a hosted service.

`kcac` is not affiliated with or endorsed by Red Hat. "Keycloak" is their
trademark, used here only to say what this tool talks to.

## Development

`make help` lists every target. The usual loop:

```console
make check              # formatting, vet and unit tests, same as CI
make kc-start           # seeded Keycloak on :8080
make test-integration   # integration and answer-key tests against it
make demo               # see dump and explain in action
make kc-stop

make kc-start-legacy    # Keycloak 22, to exercise the older group API
make test-all           # start Keycloak, run everything, stop it
make cover              # coverage across unit and integration tests
make audit              # golangci-lint, govulncheck and a secret scan
make fixture            # rebuild the seeded realm
make dist               # build release artefacts locally, exactly as a release does
```

Releases are cut by pushing a tag. GoReleaser builds every platform, writes
`checksums.txt`, publishes the GitHub release and updates the Homebrew tap:

```console
git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0
```

CI runs the same targets, so local and CI cannot drift apart. There are three
workflows: build and test (unit plus the Keycloak version matrix), lint
(golangci-lint), and security (govulncheck plus a secret scan, re-run weekly so a
new advisory against an unchanged dependency does not slip by).

Integration runs pass `-count=1` for a reason. Go caches passing test results, and
nothing it hashes changes when the Keycloak container behind `localhost:8080`
changes version, so without it a run against Keycloak 22 cheerfully reports the
cached result from Keycloak 26.

Contributions welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for what gets merged
and what does not.

The most useful contribution is a correctness report. If `kcac` gets something
wrong on your realm, `kcac fetch` writes out exactly what it read from the API.
Attach that to an issue and the bug can be reproduced without anyone needing
access to your systems. Read the file first, since it contains usernames and your
group structure.
