# Contributing to kcac

Thanks for looking. `kcac` is a small, deliberately narrow tool, and the fastest
way to help is usually to run it against a realm I have never seen and tell me
what it got wrong.

## The most useful thing you can do

Run it against your realm and check the answers.

`kcac` exists to be correct. If it reports access that a person does not have, or
misses access they do have, that is the most serious kind of bug this project can
have, and I want to know about it.

When something looks wrong:

```console
kcac fetch --url ... --realm ... --client-id ... -o snapshot.json
```

`fetch` writes out exactly what `kcac` read from the Keycloak Admin API, before it
works anything out. Attach that file to the issue and the bug can be reproduced
and fixed without anyone needing access to your systems.

**Read the file before you attach it.** It contains usernames, email addresses and
your group and role structure. Strip or rename anything you are not happy to make
public, or say so in the issue and we will find another way.

Also helpful in a bug report:

* your Keycloak version
* what `kcac` said
* what you expected, and how you know (the Keycloak admin console is fine)

## What gets merged, and what does not

`kcac` does one job: read one realm, work out who really has access, write one
file. Keeping it that small is a deliberate choice, not a lack of ambition.

**Very welcome:**

* correctness fixes, especially anything about inherited group roles or nested
  roles
* support for Keycloak versions that behave differently
* better error messages, particularly ones that tell somebody how to fix the
  problem
* performance work on large realms, as long as it does not increase load on the
  Keycloak server
* documentation, including plain corrections to my English

**Please open an issue before writing code for:**

* new output formats
* new flags
* anything that changes the CSV columns, since people diff those between review
  cycles

**Will not be merged, sorry:**

* scheduling, a daemon, or anything that runs continuously
* a database or any stored state
* a web interface
* pushing results into other systems
* writing to Keycloak in any form, including "helpful" remediation

That last one is not negotiable. `kcac` being incapable of changing your realm is
the reason people are willing to run it, and there is no code path in it that
writes. Any change that adds one will be closed.

If you want the things on that list, `kcac` is meant to feed the tool you already
have. Take the CSV and go.

## Getting set up

You need Go 1.27 and Docker.

```console
git clone https://github.com/softika/kcac && cd kcac
make check          # formatting, vet and unit tests
make kc-start       # Keycloak in Docker, seeded with the test realm
make test-all       # everything, including the tests that need a live server
make kc-stop
```

`make help` lists every target.

## The correctness bar

Anything that changes how access is worked out needs to clear three things:

1. **A unit test.** The resolution logic in `internal/resolve` is pure and does no
   I/O, so you can write a test by building a realm shape in code. Have a look at
   `internal/resolve/builder_test.go` for how.

2. **The answer key still agrees.** Keycloak can calculate effective roles itself,
   and `internal/resolve/oracle_integration_test.go` checks `kcac` against it for
   every user in the test realm. If your change makes that disagree, one of the
   two is wrong and it is worth finding out which before going further.

3. **All three Keycloak versions pass.** CI runs 22, 24 and 26. Version 22 is
   there because the group API changed in 23, and both paths have to keep working.

If you are fixing a bug, a failing test first is genuinely appreciated. It makes
it obvious we are talking about the same thing.

Adding an edge case to the seeded realm is often the right move. Edit
`test/gen/main.go`, run `make fixture`, and restart Keycloak to load it. Every case
in there has a comment saying which failure it guards against. Please keep that up.

## Style

* `make check` must pass. `make lint` must pass too, and CI enforces both.
* Comments should explain **why**, not what. There is a lot of surprising
  behaviour in the Keycloak Admin API, and a comment recording what surprised you
  will save the next person an afternoon.
* Errors should say what somebody can do about them.
* Small files, small functions.

## Licence and sign-off

`kcac` is AGPL-3.0, and the published tool is staying that way.

Two things are asked of every contribution.

**1. Sign your commits off.** This is the
[Developer Certificate of Origin](https://developercertificate.org), the same
thing the Linux kernel uses. It is you confirming you wrote the code, or otherwise
have the right to submit it. Add `-s` when you commit:

```console
git commit -s -m "fix: page subgroups past the server default"
```

That adds a `Signed-off-by` line. That is all it is. Nothing gets assigned to
anybody.

**2. Allow the project to be licensed commercially later.** You keep your
copyright. In addition to the AGPL, you grant the maintainers of `kcac` a
perpetual, worldwide, non-exclusive, royalty-free and irrevocable licence to use,
reproduce, modify and distribute your contribution, including under different
licence terms.

In plain terms: I may one day sell a commercially licensed version for companies
that cannot use AGPL software. Without this, doing that would mean tracking down
every past contributor for permission, which in practice means it never happens.
This costs you nothing, your contribution stays free and open under the AGPL
forever, and you can still do whatever you like with your own code elsewhere.

If that does not sit right with you, say so on the issue before you write
anything. I would much rather hear about it early than waste your time.

## Reporting a security problem

Please do not open a public issue for security problems.

Use GitHub's private vulnerability reporting instead. Go to the
[Security tab](https://github.com/softika/kcac/security) and choose **Report a
vulnerability**. That opens a thread only you and the maintainers can see, and it
stays private until there is a fix. No email needed, and nothing is exposed while
it is being worked on.

Please allow a reasonable window for a fix before publishing anything.

Worth knowing what is and is not in scope. The test realm in this repo contains a
hard-coded secret on purpose, because it is a throwaway fixture. That one is not a
vulnerability. Anything that could leak a real credential, or make `kcac` write to
a realm, very much is.
