package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/softika/kcac/internal/kc"
	"github.com/softika/kcac/internal/output"
	"github.com/softika/kcac/internal/resolve"
)

// countingWriter records how many bytes were written, for the manifest.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// runDump collects a realm, resolves effective access, and writes the CSV.
func runDump(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: kcac dump --url URL --realm REALM --client-id ID [flags]")
		_, _ = fmt.Fprintln(stderr, "\nWrites one CSV row per user per entitlement, with the reason each grant exists.\n\nFlags:")
		fs.PrintDefaults()
	}

	var cf connectionFlags
	cf.register(fs)
	excludeDefaults := fs.Bool("exclude-default-roles", false,
		"omit default-roles-<realm> and everything reachable only through it")
	withLastLogin := fs.Bool("with-last-login", false,
		"look up last login from the event log (often disabled or short-lived; see --events-window)")
	eventsWindow := fs.Duration("events-window", 90*24*time.Hour,
		"how far back to search the event log for logins")
	manifestPath := fs.String("manifest", "",
		"also write a JSON manifest here recording counts, versions and every limitation that applied")

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

	lastLogin := gatherLastLogin(ctx, client, snap, *withLastLogin, *eventsWindow, progress)
	opts := output.Options{LastLogin: lastLogin}

	res := resolve.Resolve(snap, resolve.Options{ExcludeDefaultRoles: *excludeDefaults})
	progress("resolved: %s", res.Stats())

	out, closeOut, err := cf.writer(stdout)
	if err != nil {
		return err
	}

	// Hash and measure the CSV as it is written, so the manifest describes
	// exactly the bytes that landed on disk.
	hasher := sha256.New()
	counter := &countingWriter{}
	if err := output.WriteCSV(io.MultiWriter(out, hasher, counter), res, opts); err != nil {
		_ = closeOut()
		return err
	}
	if err := closeOut(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}

	if *manifestPath != "" {
		m := output.BuildManifest(version, snap, res, opts, *excludeDefaults)
		m.Output = output.OutputInfo{
			File:   cf.output,
			Bytes:  counter.n,
			SHA256: hex.EncodeToString(hasher.Sum(nil)),
		}
		// Same handling as the data file: the manifest describes a realm's
		// entitlement map, and a truncated one would misreport what the run
		// covered.
		f, err := createPrivate(*manifestPath)
		if err != nil {
			return err
		}
		if err := m.Encode(f); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close manifest %s: %w", *manifestPath, err)
		}
		progress("manifest: %s (%d limitations recorded)", *manifestPath, len(m.Limitations))
	}

	reportLimitations(stderr, snap, res, lastLogin, *excludeDefaults, *manifestPath)
	return nil
}

// gatherLastLogin looks up login recency, reporting honestly when it cannot.
func gatherLastLogin(ctx context.Context, client *kc.Client, snap *kc.Snapshot,
	requested bool, window time.Duration, progress kc.Progress) output.LastLogin {

	if !requested {
		return output.LastLogin{}
	}

	ll := output.LastLogin{Requested: true, Window: window}

	settings, err := client.EventSettings(ctx)
	if err != nil {
		ll.Reason = "could not read the realm event settings: " + err.Error()
		return ll
	}
	if ok, reason := settings.RecordsLogins(); !ok {
		ll.Reason = reason
		return ll
	}

	logins, warnings, err := client.LastLogins(ctx, time.Now().Add(-window))
	if err != nil {
		ll.Reason = "could not read the event log: " + err.Error()
		return ll
	}

	ll.Available = true
	ll.ByUserID = logins
	snap.Warnings = append(snap.Warnings, warnings...)
	progress("last login: %d of %d accounts seen in the last %s", len(logins), len(snap.Users), window)
	return ll
}

// reportLimitations surfaces on stderr what the manifest records, so somebody
// running kcac interactively is not the last to know the output is partial.
func reportLimitations(stderr io.Writer, snap *kc.Snapshot, res *resolve.Result,
	ll output.LastLogin, excludedDefaults bool, manifestPath string) {

	for _, w := range snap.Warnings {
		_, _ = fmt.Fprintf(stderr, "note: %s\n", w)
	}
	for _, w := range res.Warnings {
		_, _ = fmt.Fprintf(stderr, "note: %s\n", w)
	}
	if ll.Requested && !ll.Available {
		_, _ = fmt.Fprintf(stderr, "note: last_login is empty for every account: %s\n", ll.Reason)
	}

	// Offer the noise filter rather than applying it silently: dropping data a
	// reviewer did not ask to drop is the wrong default.
	if !excludedDefaults && hasDefaultRoleGrants(res, snap.Realm) {
		_, _ = fmt.Fprintf(stderr,
			"note: output includes default-roles-%s, which every account holds; pass --exclude-default-roles to omit it\n",
			snap.Realm)
	}
	if manifestPath == "" {
		_, _ = fmt.Fprintln(stderr, "note: no --manifest was written, so nothing records what this run could not determine")
	}
}

func hasDefaultRoleGrants(res *resolve.Result, realm string) bool {
	want := "default-roles-" + realm
	for _, g := range res.Grants {
		if !g.Entitlement.IsClientRole() && g.Entitlement.Name == want {
			return true
		}
	}
	return false
}
