package kc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// backoffBase is the first retry delay; subsequent delays double it.
	backoffBase = 250 * time.Millisecond
	// backoffCap bounds a single delay so a long Retry-After or a deep retry
	// count cannot stall a collection indefinitely.
	backoffCap = 30 * time.Second
	// maxReauths bounds 401-triggered re-authentication per request, so bad
	// credentials fail fast instead of looping.
	maxReauths = 1
	// errBodyLimit bounds how much of an error response is read into a message.
	errBodyLimit = 2 << 10
)

// APIError is a non-2xx response from the Admin API.
type APIError struct {
	StatusCode int
	Status     string
	URL        string
	Body       string

	// retryAfter carries a server-supplied Retry-After, if any.
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("keycloak returned %s for %s", e.Status, e.URL)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// IsNotFound reports whether err is a 404. Used for capability detection: kcac
// probes GET /groups/{id}/children and falls back to nested subGroups when the
// server predates Keycloak 23, rather than parsing version strings.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// IsForbidden reports whether err is a 403, which in practice means the service
// account is missing one of the required read-only roles.
func IsForbidden(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden
}

// IsEndpointAbsent reports whether err means "this server does not offer that
// endpoint", which is how kcac detects capabilities.
//
// Both 404 and 405 have to count. Keycloak 22 answers GET
// /groups/{id}/children with 405 Method Not Allowed rather than 404, because the
// path exists there for POST (create a child group) and only the GET was added
// in 23.0.0 — so RESTEasy reports a missing method, not a missing resource.
// Treating only 404 as absence makes the fallback fail on exactly the servers
// that need it. Verified against quay.io/keycloak/keycloak:22.0.
func IsEndpointAbsent(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusMethodNotAllowed
}

// Client reads one Keycloak realm over the Admin API.
//
// Every request is bounded by the configured concurrency, retried with
// exponential backoff and jitter on transient failures, and carries a
// per-request timeout. A Client is safe for concurrent use.
type Client struct {
	cfg        Config
	httpClient *http.Client
	tokens     *tokenManager
	sem        chan struct{}

	// children caches whether this server exposes /groups/{id}/children, so the
	// capability is probed once per run rather than per group.
	children childrenModeState

	// Injected in tests so retry behaviour is verifiable without real delays.
	sleep  func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

// New builds a Client. It validates cfg and performs no network I/O.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.WithDefaults()

	// DefaultTransport is documented as an *http.Transport, but a library or a
	// test can replace it. Failing with a clear message beats a panic deep in a
	// collection against a customer's server.
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("kc: expected http.DefaultTransport to be *http.Transport, got %T", http.DefaultTransport)
	}
	transport := base.Clone()
	transport.MaxIdleConnsPerHost = cfg.Concurrency
	if cfg.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in, warned about by the caller
	}

	httpClient := &http.Client{Transport: transport}

	return &Client{
		cfg:        cfg,
		httpClient: httpClient,
		tokens:     newTokenManager(cfg, httpClient),
		sem:        make(chan struct{}, cfg.Concurrency),
		sleep:      sleepCtx,
		jitter:     defaultJitter,
	}, nil
}

// Config returns the effective configuration, defaults applied.
func (c *Client) Config() Config { return c.cfg }

// sleepCtx waits for d or until ctx is done, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// defaultJitter spreads retries so concurrent workers do not resynchronise into
// a thundering herd against a server that is already struggling.
func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// Spreading retries is a fairness concern, not a security one, so a weak
	// RNG is the right tool here.
	return d + rand.N(d/2+1) //nolint:gosec // G404: jitter, not a secret
}

// getJSON performs a GET against rawURL and decodes the JSON body into out.
//
// out may be nil to discard the body. The request is bounded by the client's
// concurrency limit, retried on 429/5xx/network errors, and re-authenticated
// once on 401.
func (c *Client) getJSON(ctx context.Context, rawURL string, out any) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	var (
		attempt int
		reauths int
		lastErr error
	)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		body, err := c.attempt(ctx, rawURL)
		if err == nil {
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("decode response from %s: %w", rawURL, err)
			}
			return nil
		}
		lastErr = err

		// A 401 mid-collection usually means the token aged out or was revoked.
		// Re-authenticate once; a second 401 is a real authorisation problem.
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized && reauths < maxReauths {
			reauths++
			c.tokens.Invalidate()
			continue
		}

		if !isRetryable(err) || attempt >= c.cfg.MaxRetries {
			return lastErr
		}

		delay := c.retryDelay(attempt, err)
		if sleepErr := c.sleep(ctx, delay); sleepErr != nil {
			// Both are wrapped: callers match the context error to detect an
			// abort, and the API error to see what was actually failing.
			return fmt.Errorf("%w (while backing off after: %w)", sleepErr, lastErr)
		}
		attempt++
	}
}

// attempt performs exactly one request and returns the body on success.
func (c *Client) attempt(ctx context.Context, rawURL string) ([]byte, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", rawURL, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &transportError{URL: rawURL, err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return nil, &APIError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			URL:        rawURL,
			Body:       strings.TrimSpace(string(snippet)),
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", rawURL, err)
	}
	return body, nil
}

// transportError is a network-level failure, always retryable.
type transportError struct {
	URL string
	err error
}

func (e *transportError) Error() string { return fmt.Sprintf("request to %s failed: %v", e.URL, e.err) }
func (e *transportError) Unwrap() error { return e.err }

// isRetryable reports whether err is worth another attempt. 429 and 5xx are
// transient by definition; 4xx other than 429 will not change on retry.
func isRetryable(err error) bool {
	var transportErr *transportError
	if errors.As(err, &transportErr) {
		// A cancelled or timed-out parent context is not worth retrying.
		return !errors.Is(err, context.Canceled)
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= 500
	}
	return false
}

// retryDelay honours a server-supplied Retry-After when present, and otherwise
// backs off exponentially with jitter.
func (c *Client) retryDelay(attempt int, err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.retryAfter > 0 {
		return min(apiErr.retryAfter, backoffCap)
	}

	exp := float64(backoffBase) * math.Pow(2, float64(attempt))
	delay := time.Duration(min(exp, float64(backoffCap)))
	return min(c.jitter(delay), backoffCap)
}

// parseRetryAfter understands both forms RFC 9110 allows: delay-seconds and an
// HTTP-date.
func parseRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(header); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// query appends encoded parameters to a URL.
func query(rawURL string, params url.Values) string {
	if len(params) == 0 {
		return rawURL
	}
	if strings.Contains(rawURL, "?") {
		return rawURL + "&" + params.Encode()
	}
	return rawURL + "?" + params.Encode()
}
