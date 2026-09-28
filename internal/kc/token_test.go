package kc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tokenServer returns a stub token endpoint and a counter of how many times it
// was called.
func tokenServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int32)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		handler(w, r, n)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newTestTokenManager(t *testing.T, srv *httptest.Server, clock func() time.Time) *tokenManager {
	t.Helper()
	cfg := Config{BaseURL: srv.URL, Realm: "test", ClientID: "kcac-audit", ClientSecret: "s3cret"}.WithDefaults()
	tm := newTokenManager(cfg, srv.Client())
	if clock != nil {
		tm.now = clock
	}
	return tm
}

func TestTokenManagerFetchesAndSendsCorrectForm(t *testing.T) {
	var gotGrant, gotID, gotSecret, gotContentType string
	srv, calls := tokenServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		_ = r.ParseForm()
		gotGrant = r.Form.Get("grant_type")
		gotID = r.Form.Get("client_id")
		gotSecret = r.Form.Get("client_secret")
		gotContentType = r.Header.Get("Content-Type")
		fmt.Fprint(w, `{"access_token":"tok-1","expires_in":60,"token_type":"Bearer"}`)
	})

	tm := newTestTokenManager(t, srv, nil)
	got, err := tm.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}

	if got != "tok-1" {
		t.Errorf("token = %q, want tok-1", got)
	}
	if calls.Load() != 1 {
		t.Errorf("token endpoint called %d times, want 1", calls.Load())
	}
	if gotGrant != "client_credentials" {
		t.Errorf("grant_type = %q, want client_credentials", gotGrant)
	}
	if gotID != "kcac-audit" || gotSecret != "s3cret" {
		t.Errorf("credentials not sent: id=%q secret=%q", gotID, gotSecret)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
}

func TestTokenManagerReusesTokenWithinRefreshWindow(t *testing.T) {
	srv, calls := tokenServer(t, func(w http.ResponseWriter, _ *http.Request, n int32) {
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":100}`, n)
	})

	base := time.Now()
	clock := base
	tm := newTestTokenManager(t, srv, func() time.Time { return clock })

	first, _ := tm.Token(context.Background())

	// 79s of a 100s token: inside the 80% window, must be cached.
	clock = base.Add(79 * time.Second)
	second, err := tm.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if second != first {
		t.Errorf("token changed inside refresh window: %q then %q", first, second)
	}
	if calls.Load() != 1 {
		t.Errorf("token endpoint called %d times, want 1", calls.Load())
	}
}

func TestTokenManagerRefreshesAtEightyPercentOfLifetime(t *testing.T) {
	srv, calls := tokenServer(t, func(w http.ResponseWriter, _ *http.Request, n int32) {
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":100}`, n)
	})

	base := time.Now()
	clock := base
	tm := newTestTokenManager(t, srv, func() time.Time { return clock })

	first, _ := tm.Token(context.Background())

	// 81s of a 100s token: past 80%, so refresh even though the token is still
	// technically valid. This is the whole point — never fail mid-collection.
	clock = base.Add(81 * time.Second)
	second, err := tm.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if second == first {
		t.Error("token should have been refreshed past 80% of its lifetime")
	}
	if calls.Load() != 2 {
		t.Errorf("token endpoint called %d times, want 2", calls.Load())
	}
}

func TestTokenManagerInvalidateForcesReauth(t *testing.T) {
	srv, calls := tokenServer(t, func(w http.ResponseWriter, _ *http.Request, n int32) {
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, n)
	})
	tm := newTestTokenManager(t, srv, nil)

	first, _ := tm.Token(context.Background())
	tm.Invalidate()
	second, err := tm.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}

	if first == second {
		t.Error("Invalidate() should force a new token")
	}
	if calls.Load() != 2 {
		t.Errorf("token endpoint called %d times, want 2", calls.Load())
	}
}

func TestTokenManagerDoesNotCacheWhenNoExpiryAdvertised(t *testing.T) {
	srv, calls := tokenServer(t, func(w http.ResponseWriter, _ *http.Request, n int32) {
		fmt.Fprintf(w, `{"access_token":"tok-%d"}`, n)
	})
	tm := newTestTokenManager(t, srv, nil)

	_, _ = tm.Token(context.Background())
	_, _ = tm.Token(context.Background())

	// Guessing a lifetime risks a mid-collection 401; refetching is cheap.
	if calls.Load() != 2 {
		t.Errorf("token endpoint called %d times, want 2 (no caching without expires_in)", calls.Load())
	}
}

func TestTokenManagerConcurrentCallersCauseOneFetch(t *testing.T) {
	srv, calls := tokenServer(t, func(w http.ResponseWriter, _ *http.Request, n int32) {
		time.Sleep(10 * time.Millisecond) // widen the race window
		fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, n)
	})
	tm := newTestTokenManager(t, srv, nil)

	const workers = 24
	var wg sync.WaitGroup
	tokens := make([]string, workers)
	errs := make([]error, workers)
	wg.Add(workers)
	for i := range workers {
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = tm.Token(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		if tokens[i] != tokens[0] {
			t.Errorf("worker %d got %q, want all workers to share %q", i, tokens[i], tokens[0])
		}
	}
	if calls.Load() != 1 {
		t.Errorf("token endpoint called %d times, want 1", calls.Load())
	}
}

func TestTokenManagerErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantHint string
	}{
		{
			name:     "bad secret",
			status:   http.StatusUnauthorized,
			body:     `{"error":"invalid_client","error_description":"Invalid client credentials"}`,
			wantHint: "check the client id and secret",
		},
		{
			name:     "service account disabled",
			status:   http.StatusBadRequest,
			body:     `{"error":"unauthorized_client"}`,
			wantHint: "Service accounts roles is disabled",
		},
		{
			name:     "realm not found",
			status:   http.StatusNotFound,
			body:     `not found`,
			wantHint: "realm not found at this URL",
		},
		{
			name:     "malformed json",
			status:   http.StatusOK,
			body:     `<html>proxy error</html>`,
			wantHint: "not valid JSON",
		},
		{
			name:     "empty token",
			status:   http.StatusOK,
			body:     `{"expires_in":60}`,
			wantHint: "contained no access_token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := tokenServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			})
			tm := newTestTokenManager(t, srv, nil)

			_, err := tm.Token(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantHint)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("error message leaked the client secret: %q", err)
			}
		})
	}
}

func TestTokenManagerRespectsContextCancellation(t *testing.T) {
	// The handler must not outlive the test: a client-side context cancellation
	// aborts the client but leaves the server handler running, and
	// httptest.Server.Close blocks on in-flight handlers. release is closed
	// before srv.Close because t.Cleanup runs last-registered-first.
	release := make(chan struct{})

	srv, _ := tokenServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })

	tm := newTestTokenManager(t, srv, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := tm.Token(ctx); err == nil {
		t.Fatal("expected a context error")
	}
}

func TestTokenRequestIsBoundedByRequestTimeout(t *testing.T) {
	// A token endpoint that accepts the connection and then never answers must
	// not stall the run. The CLI's context carries no deadline, and the mutex is
	// held across the request, so without its own timeout one unresponsive
	// endpoint blocks every worker indefinitely.
	release := make(chan struct{})

	srv, _ := tokenServer(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(func() { close(release) })

	cfg := Config{
		BaseURL: srv.URL, Realm: "test", ClientID: "kcac-audit", ClientSecret: "s3cret",
		RequestTimeout: 100 * time.Millisecond,
	}.WithDefaults()
	tm := newTokenManager(cfg, srv.Client())

	start := time.Now()
	// context.Background() deliberately: no deadline from the caller, exactly
	// like the CLI.
	_, err := tm.Token(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the token request to time out")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("took %v to give up; --timeout does not cover authentication", elapsed)
	}
}

func TestGetJSONTimesOutWhenTokenEndpointHangs(t *testing.T) {
	// The same thing through the client, which is how it would actually happen.
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	c, err := New(Config{
		BaseURL: srv.URL, Realm: "test", ClientID: "kcac-audit", ClientSecret: "s3cret",
		RequestTimeout: 100 * time.Millisecond,
		MaxRetries:     1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	c.httpClient = srv.Client()
	c.tokens = newTokenManager(c.cfg, srv.Client())
	c.sleep = func(context.Context, time.Duration) error { return nil }

	done := make(chan error, 1)
	go func() { done <- c.getJSON(context.Background(), srv.URL+"/admin/realms/test/users", nil) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("getJSON hung waiting on the token endpoint")
	}
}
