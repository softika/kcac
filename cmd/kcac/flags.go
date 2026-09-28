package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/softika/kcac/internal/kc"
)

// secretEnv is the preferred way to supply the client secret: passing it as a
// flag puts it in the process table where any local user can read it.
const secretEnv = "KCAC_CLIENT_SECRET" //nolint:gosec // G101: the NAME of an env var, not a credential

// connectionFlags are the flags every kcac subcommand shares.
type connectionFlags struct {
	url          string
	realm        string
	clientID     string
	clientSecret string
	secretFile   string

	concurrency int
	pageSize    int
	timeout     time.Duration
	maxRetries  int
	insecure    bool

	output string
	quiet  bool
}

func (f *connectionFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.url, "url", "", "Keycloak base URL, e.g. https://kc.example.com (required)")
	fs.StringVar(&f.realm, "realm", "", "realm name to read (required)")
	fs.StringVar(&f.clientID, "client-id", "", "confidential client id whose service account reads the realm (required)")
	fs.StringVar(&f.clientSecret, "client-secret", "", "client secret; prefer "+secretEnv+" so it stays out of the process table")
	fs.StringVar(&f.secretFile, "client-secret-file", "", "read the client secret from this file")

	fs.IntVar(&f.concurrency, "concurrency", kc.DefaultConcurrency, "maximum concurrent requests against Keycloak")
	fs.IntVar(&f.pageSize, "page-size", kc.DefaultPageSize, "page size for paginated endpoints")
	fs.DurationVar(&f.timeout, "timeout", kc.DefaultRequestTimeout, "per-request timeout")
	fs.IntVar(&f.maxRetries, "max-retries", kc.DefaultMaxRetries, "retries per request on 429 and 5xx")
	fs.BoolVar(&f.insecure, "insecure", false, "skip TLS verification (for a private CA; prefer trusting the CA)")

	fs.StringVar(&f.output, "o", "", "write output to this file instead of stdout")
	fs.BoolVar(&f.quiet, "quiet", false, "suppress progress output on stderr")
}

// resolveSecret applies the precedence env > file > flag and warns when the
// least safe option was used.
func (f *connectionFlags) resolveSecret(stderr io.Writer) error {
	if env := os.Getenv(secretEnv); env != "" {
		f.clientSecret = env
		return nil
	}

	if f.secretFile != "" {
		data, err := os.ReadFile(f.secretFile)
		if err != nil {
			return fmt.Errorf("read --client-secret-file: %w", err)
		}
		f.clientSecret = strings.TrimSpace(string(data))
		if f.clientSecret == "" {
			return fmt.Errorf("--client-secret-file %s is empty", f.secretFile)
		}
		return nil
	}

	if f.clientSecret != "" {
		_, _ = fmt.Fprintf(stderr,
			"warning: --client-secret is visible to other local users via the process list; prefer %s or --client-secret-file\n",
			secretEnv)
	}
	return nil
}

// config turns parsed flags into a validated kc.Config.
func (f *connectionFlags) config(stderr io.Writer) (kc.Config, error) {
	if err := f.resolveSecret(stderr); err != nil {
		return kc.Config{}, err
	}
	if f.insecure {
		_, _ = fmt.Fprintln(stderr, "warning: --insecure disables TLS verification; traffic can be intercepted")
	}

	cfg := kc.Config{
		BaseURL:            f.url,
		Realm:              f.realm,
		ClientID:           f.clientID,
		ClientSecret:       f.clientSecret,
		Concurrency:        f.concurrency,
		PageSize:           f.pageSize,
		RequestTimeout:     f.timeout,
		MaxRetries:         f.maxRetries,
		InsecureSkipVerify: f.insecure,
	}
	if err := cfg.Validate(); err != nil {
		return kc.Config{}, err
	}
	return cfg, nil
}

// progress returns a reporter that writes to stderr, keeping stdout a clean
// data stream so kcac can be piped.
func (f *connectionFlags) progress(stderr io.Writer) kc.Progress {
	if f.quiet {
		return func(string, ...any) {}
	}
	return func(format string, args ...any) {
		_, _ = fmt.Fprintf(stderr, format+"\n", args...)
	}
}

// writer returns the destination for data output, plus a close func.
func (f *connectionFlags) writer(stdout io.Writer) (io.Writer, func() error, error) {
	if f.output == "" {
		return stdout, func() error { return nil }, nil
	}
	file, err := createPrivate(f.output)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

// createPrivate opens path for writing with owner-only permissions.
//
// The output is a complete map of who can do what in a realm, and kcac is often
// run on a shared build host or a jump box where the default umask would leave
// it readable by every local account. The same care taken to keep the client
// secret out of the process list applies one step later, to what that secret was
// used to produce.
//
// The mode passed to OpenFile only takes effect when the file is created, so a
// path an earlier run left at 0644 would silently keep it. The permissions are
// therefore set explicitly.
//
// Truncation happens last, on purpose: if the permissions cannot be fixed, the
// previous report is still intact rather than destroyed on the way to an error.
func createPrivate(path string) (*os.File, error) {
	// The path is whatever the operator passed to -o or --manifest, and kcac runs
	// with their privileges and writes where they asked. No boundary is crossed.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600) //nolint:gosec // G304: operator-supplied output path is the point
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}

	// Only regular files carry meaningful permissions. Writing to /dev/null, a
	// named pipe or a device should keep working.
	if info.Mode().IsRegular() && info.Mode().Perm() != 0o600 {
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("could not restrict permissions on %s, refusing to write a realm's access map to a file others can read: %w", path, err)
		}
	}

	if info.Mode().IsRegular() {
		if err := file.Truncate(0); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("truncate %s: %w", path, err)
		}
	}

	return file, nil
}
