package kc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// refreshFraction is the portion of a token's advertised lifetime kcac will use
// before refreshing. A long collection against a large realm routinely outlives
// a single token, and a mid-run expiry surfaces as a confusing 401 rather than
// an auth problem, so kcac refreshes early rather than on failure.
const refreshFraction = 0.8

// tokenManager fetches and caches a client_credentials access token for one
// realm. It is safe for concurrent use: callers that arrive during a refresh
// block and then observe the fresh token, so N concurrent workers cause one
// token request rather than N.
type tokenManager struct {
	httpClient *http.Client
	tokenURL   string
	clientID   string
	secret     string

	// requestTimeout bounds the token request itself. Without it the POST
	// inherits only the caller's context, which for the CLI has no deadline, and
	// because the mutex is held across the request a token endpoint that accepts
	// a connection and never answers stalls every worker for as long as it
	// wants. --timeout has to cover authentication too, not just the calls after
	// it.
	requestTimeout time.Duration

	// now is injectable so token expiry is testable without sleeping.
	now func() time.Time

	mu        sync.Mutex
	token     string
	refreshAt time.Time
}

func newTokenManager(cfg Config, httpClient *http.Client) *tokenManager {
	return &tokenManager{
		httpClient:     httpClient,
		tokenURL:       cfg.tokenURL(),
		clientID:       cfg.ClientID,
		secret:         cfg.ClientSecret,
		requestTimeout: cfg.RequestTimeout,
		now:            time.Now,
	}
}

// Token returns a valid access token, fetching or refreshing as needed.
func (t *tokenManager) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && t.now().Before(t.refreshAt) {
		return t.token, nil
	}
	if err := t.fetchLocked(ctx); err != nil {
		return "", err
	}
	return t.token, nil
}

// Invalidate discards the cached token so the next Token call re-authenticates.
// Used when the server rejects a request with 401 mid-collection, which can
// happen if the token was revoked or the clock skewed.
func (t *tokenManager) Invalidate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.token = ""
	t.refreshAt = time.Time{}
}

// fetchLocked performs the token request. Callers must hold t.mu.
func (t *tokenManager) fetchLocked(ctx context.Context) error {
	if t.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.requestTimeout)
		defer cancel()
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {t.clientID},
		"client_secret": {t.secret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("token request to %s: %w", t.tokenURL, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	// Read a bounded amount: an HTML error page from a proxy should not be
	// slurped whole into an error message.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Deliberately does not echo the request body: it contains the secret.
		return fmt.Errorf("authentication failed: %s returned %s: %s",
			t.tokenURL, resp.Status, describeAuthFailure(resp.StatusCode, body))
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("token response from %s is not valid JSON: %w", t.tokenURL, err)
	}
	if payload.AccessToken == "" {
		return fmt.Errorf("token response from %s contained no access_token", t.tokenURL)
	}

	t.token = payload.AccessToken
	if payload.ExpiresIn > 0 {
		lifetime := time.Duration(float64(payload.ExpiresIn) * refreshFraction * float64(time.Second))
		t.refreshAt = t.now().Add(lifetime)
	} else {
		// No advertised lifetime: do not cache, refetch on every request rather
		// than guess and fail mid-collection.
		t.refreshAt = time.Time{}
	}
	return nil
}

// describeAuthFailure turns a token endpoint rejection into advice, because the
// two common causes have completely different fixes.
func describeAuthFailure(status int, body []byte) string {
	var oauthErr struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &oauthErr)

	switch {
	case oauthErr.Error == "invalid_client" || status == http.StatusUnauthorized:
		return "check the client id and secret, and that the client has Client authentication enabled (a confidential client)"
	case oauthErr.Error == "unauthorized_client":
		return "the client exists but Service accounts roles is disabled for it"
	case status == http.StatusNotFound:
		return "realm not found at this URL — check the realm name and whether the server uses a legacy /auth path"
	case oauthErr.Description != "":
		return oauthErr.Description
	case oauthErr.Error != "":
		return oauthErr.Error
	default:
		return "unexpected response from the token endpoint"
	}
}
