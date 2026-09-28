package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/output"
	"github.com/softika/kcac/internal/resolve"
)

// runExplain shows every route by which one user holds every entitlement.
//
// dump shows the single simplest explanation per entitlement, because a reviewer
// facing the same role four times stops reading. explain shows all of them, which
// is what is needed when auditing one account or settling an argument about why a
// grant exists.
func runExplain(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: kcac explain --url URL --realm REALM --client-id ID [flags] USERNAME")
		_, _ = fmt.Fprintln(stderr, "\nShows every path by which one user holds every entitlement.\n\nFlags:")
		fs.PrintDefaults()
	}

	var cf connectionFlags
	cf.register(fs)
	excludeDefaults := fs.Bool("exclude-default-roles", false,
		"omit default-roles-<realm> and everything reachable only through it")

	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return fmt.Errorf("expected exactly one username, got %d", len(rest))
	}
	username := rest[0]

	cfg, err := cf.config(stderr)
	if err != nil {
		return err
	}
	client, err := kc.New(cfg)
	if err != nil {
		return err
	}

	progress := cf.progress(stderr)
	snap, err := kc.Collect(ctx, client, progress)
	if err != nil {
		return err
	}

	res := resolve.Resolve(snap, resolve.Options{ExcludeDefaultRoles: *excludeDefaults})

	out, closeOut, err := cf.writer(stdout)
	if err != nil {
		return err
	}

	if err := output.WriteExplain(out, res, username); err != nil {
		_ = closeOut()
		return err
	}
	// WriteExplain reports its own write failures rather than discarding them,
	// so discarding this one would defeat the point.
	if err := closeOut(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}

	for _, w := range res.Warnings {
		_, _ = fmt.Fprintf(stderr, "note: %s\n", w)
	}
	return nil
}
