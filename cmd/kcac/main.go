// Command kcac exports effective access from a Keycloak realm, showing not just
// who holds which entitlement but why they hold it.
//
// kcac is strictly read-only: it issues GET requests plus the OAuth2 token POST
// needed to authenticate, and never modifies a realm.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

// Build metadata, stamped by the linker at release time. A bug report that names
// the exact commit is worth a great deal more than one that says "latest".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const usage = `kcac %s — effective access export for Keycloak

Usage:
  kcac <command> [flags]

Commands:
  dump      write effective access as CSV, with the reason each grant exists
  explain   show every path by which one user holds every entitlement
  fetch     collect a realm and write the raw snapshot as JSON (diagnostics)
  version   print the version

Run "kcac <command> -h" for the flags of a command.

kcac is read-only. It never writes to Keycloak.

The service account of --client-id needs these realm-management roles and
nothing more:
  view-users  view-clients  view-realm  query-users  query-groups

Supply the secret via %s rather than --client-secret, which is
visible to other local users in the process list.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(os.Stderr, "kcac: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, usage, version, secretEnv)
		return flag.ErrHelp
	}

	// Ctrl-C cancels an in-flight collection promptly rather than leaving
	// requests hanging against the customer's server.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd := args[0]; cmd {
	case "dump":
		return runDump(ctx, args[1:], os.Stdout, os.Stderr)

	case "explain":
		return runExplain(ctx, args[1:], os.Stdout, os.Stderr)

	case "fetch":
		return runFetch(ctx, args[1:], os.Stdout, os.Stderr)

	case "version", "--version", "-version":
		_, _ = fmt.Fprintf(os.Stdout, "kcac %s\ncommit: %s\nbuilt:  %s\n%s\n",
			version, commit, date, runtime.Version())
		return nil

	case "help", "-h", "--help":
		_, _ = fmt.Fprintf(os.Stderr, usage, version, secretEnv)
		return flag.ErrHelp

	default:
		_, _ = fmt.Fprintf(os.Stderr, "kcac: unknown command %q\n\n", cmd)
		_, _ = fmt.Fprintf(os.Stderr, usage, version, secretEnv)
		return flag.ErrHelp
	}
}
