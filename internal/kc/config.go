// Package kc is the Keycloak Admin API client: authentication, pagination,
// retry and capability detection. It performs every network call kcac makes and
// contains no access-resolution logic — that lives in internal/resolve, which is
// pure and I/O-free so the correctness bar is testable without a server.
//
// Every request this package issues is a read-only GET, with the single
// exception of the OAuth2 token POST required to authenticate. kcac never
// modifies a Keycloak realm.
package kc

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Defaults chosen to be safe against a production realm rather than fast.
// The first run kcac makes against a customer's Keycloak is the demo, so it must
// not degrade their service.
const (
	DefaultConcurrency    = 8
	DefaultRequestTimeout = 30 * time.Second
	DefaultMaxRetries     = 4
	DefaultPageSize       = 100
)

// Config describes how to reach one Keycloak realm.
//
// Config is a value type and every method returning a Config returns a new copy;
// nothing here mutates in place.
type Config struct {
	// BaseURL is the Keycloak root, e.g. https://kc.example.com. A legacy
	// /auth suffix is accepted and preserved.
	BaseURL string
	// Realm is the realm to read. Note this is the realm NAME, not its id.
	Realm string
	// ClientID and ClientSecret authenticate a confidential client whose
	// service account holds the read-only roles listed in the README.
	ClientID     string
	ClientSecret string

	// Concurrency bounds in-flight requests against the server.
	Concurrency int
	// RequestTimeout applies per request, not to the whole collection.
	RequestTimeout time.Duration
	// MaxRetries bounds retries of a single retryable request.
	MaxRetries int
	// PageSize is the explicit max passed on every paginated endpoint. kcac
	// never relies on a server-side default: they differ per endpoint (100 for
	// /users, 10 for /groups/{id}/children) and silently truncate.
	PageSize int

	// InsecureSkipVerify disables TLS verification. Present because corporate
	// Keycloak deployments commonly use a private CA; it is never the default
	// and callers are expected to warn when it is set.
	InsecureSkipVerify bool
}

// ErrMissingField reports a Config field that must be set.
var ErrMissingField = errors.New("kc: missing required field")

// Validate checks c at the system boundary, before any network call, and
// returns every problem at once rather than one per round trip.
func (c Config) Validate() error {
	var problems []string

	if strings.TrimSpace(c.BaseURL) == "" {
		problems = append(problems, "url is required (e.g. https://kc.example.com)")
	} else {
		u, err := url.Parse(c.BaseURL)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("url is not parseable: %v", err))
		case u.Scheme != "http" && u.Scheme != "https":
			problems = append(problems, fmt.Sprintf("url scheme must be http or https, got %q", u.Scheme))
		case u.Host == "":
			problems = append(problems, "url has no host")
		}
	}
	if strings.TrimSpace(c.Realm) == "" {
		problems = append(problems, "realm is required")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		problems = append(problems, "client-id is required")
	}
	if strings.TrimSpace(c.ClientSecret) == "" {
		problems = append(problems, "client secret is required (set KCAC_CLIENT_SECRET)")
	}
	if c.Concurrency < 0 {
		problems = append(problems, "concurrency must not be negative")
	}
	if c.PageSize < 0 {
		problems = append(problems, "page-size must not be negative")
	}
	if c.MaxRetries < 0 {
		problems = append(problems, "max-retries must not be negative")
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrMissingField, strings.Join(problems, "; "))
	}
	return nil
}

// WithDefaults returns a copy of c with unset tunables filled in. It does not
// modify c.
func (c Config) WithDefaults() Config {
	out := c
	out.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	out.Realm = strings.TrimSpace(c.Realm)
	out.ClientID = strings.TrimSpace(c.ClientID)
	if out.Concurrency == 0 {
		out.Concurrency = DefaultConcurrency
	}
	if out.RequestTimeout == 0 {
		out.RequestTimeout = DefaultRequestTimeout
	}
	if out.MaxRetries == 0 {
		out.MaxRetries = DefaultMaxRetries
	}
	if out.PageSize == 0 {
		out.PageSize = DefaultPageSize
	}
	return out
}

// adminPath builds a path under /admin/realms/{realm}.
func (c Config) adminPath(format string, args ...any) string {
	return fmt.Sprintf("%s/admin/realms/%s%s", c.BaseURL, url.PathEscape(c.Realm), fmt.Sprintf(format, args...))
}

// tokenURL is the OpenID Connect token endpoint for the configured realm.
func (c Config) tokenURL() string {
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.BaseURL, url.PathEscape(c.Realm))
}
