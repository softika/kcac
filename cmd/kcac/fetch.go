package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/softika/kcac/internal/kc"
)

// runFetch collects a realm and writes the raw snapshot as JSON.
//
// This is a diagnostic command, not the product: it exposes exactly what kcac
// read from the Admin API before any resolution, which is what makes a bug
// report about a real realm actionable without access to that realm.
func runFetch(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: kcac fetch --url URL --realm REALM --client-id ID [flags]")
		_, _ = fmt.Fprintln(stderr, "\nCollects a realm read-only and writes the raw snapshot as JSON.\n\nFlags:")
		fs.PrintDefaults()
	}

	var cf connectionFlags
	cf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

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
	progress("collected: %s", snap.Stats())
	for _, w := range snap.Warnings {
		_, _ = fmt.Fprintf(stderr, "note: %s\n", w)
	}

	out, closeOut, err := cf.writer(stdout)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		_ = closeOut()
		return fmt.Errorf("write snapshot: %w", err)
	}
	// Checked, not deferred and discarded: a Close that fails on a full disk
	// leaves a truncated snapshot, and exiting 0 would present it as complete.
	if err := closeOut(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	return nil
}
