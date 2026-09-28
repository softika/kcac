package kc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetJSONDecodesAndAuthenticates(t *testing.T) {
	var gotAuth, gotAccept string
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		fmt.Fprint(w, `{"count":42}`)
	})
	c, _ := fk.client(t)

	var out struct{ Count int }
	if err := c.getJSON(context.Background(), fk.adminURL("/users/count"), &out); err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}

	if out.Count != 42 {
		t.Errorf("Count = %d, want 42", out.Count)
	}
	if gotAuth != "Bearer tok-1" {
		t.Errorf("Authorization = %q, want Bearer tok-1", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
}

func TestGetJSONRetriesServerErrorsThenSucceeds(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, call int32) {
		if call < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	c, slept := fk.client(t)

	var out struct{ OK bool }
	if err := c.getJSON(context.Background(), fk.adminURL("/users"), &out); err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}
	if !out.OK {
		t.Error("expected the third attempt to succeed")
	}
	if fk.adminCalls.Load() != 3 {
		t.Errorf("admin called %d times, want 3", fk.adminCalls.Load())
	}

	// Exponential: 250ms then 500ms, with jitter stubbed out.
	want := []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}
	if len(*slept) != len(want) {
		t.Fatalf("slept %v, want %v", *slept, want)
	}
	for i, d := range want {
		if (*slept)[i] != d {
			t.Errorf("backoff[%d] = %v, want %v", i, (*slept)[i], d)
		}
	}
}

func TestGetJSONHonoursRetryAfterSeconds(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, call int32) {
		if call == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	c, slept := fk.client(t)

	if err := c.getJSON(context.Background(), fk.adminURL("/users"), nil); err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}
	if len(*slept) != 1 || (*slept)[0] != 7*time.Second {
		t.Errorf("slept %v, want [7s] from Retry-After", *slept)
	}
}

func TestGetJSONCapsRetryAfter(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, call int32) {
		if call == 1 {
			w.Header().Set("Retry-After", "86400") // a day
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{}`)
	})
	c, slept := fk.client(t)

	if err := c.getJSON(context.Background(), fk.adminURL("/users"), nil); err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}
	if (*slept)[0] != backoffCap {
		t.Errorf("slept %v, want it capped at %v", (*slept)[0], backoffCap)
	}
}

func TestGetJSONExhaustsRetriesAndReturnsAPIError(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "upstream down")
	})
	c, _ := fk.client(t, func(cfg *Config) { cfg.MaxRetries = 2 })

	err := c.getJSON(context.Background(), fk.adminURL("/users"), nil)
	if err == nil {
		t.Fatal("expected an error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("error should include the body snippet, got %q", err)
	}
	// MaxRetries=2 means 3 attempts total.
	if fk.adminCalls.Load() != 3 {
		t.Errorf("admin called %d times, want 3", fk.adminCalls.Load())
	}
}

func TestGetJSONReauthenticatesOnceOn401(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, call int32) {
		// Reject the first token, accept the second.
		if r.Header.Get("Authorization") == "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
		_ = call
	})
	c, slept := fk.client(t)

	var out struct{ OK bool }
	if err := c.getJSON(context.Background(), fk.adminURL("/users"), &out); err != nil {
		t.Fatalf("getJSON() error = %v", err)
	}
	if !out.OK {
		t.Error("expected success after re-authentication")
	}
	if fk.tokenCalls.Load() != 2 {
		t.Errorf("token endpoint called %d times, want 2", fk.tokenCalls.Load())
	}
	if len(*slept) != 0 {
		t.Errorf("re-auth should not back off, slept %v", *slept)
	}
}

func TestGetJSONGivesUpOnPersistent401(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c, _ := fk.client(t)

	err := c.getJSON(context.Background(), fk.adminURL("/users"), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	// One initial attempt plus exactly one re-auth: no loop.
	if got := fk.adminCalls.Load(); got != 2 {
		t.Errorf("admin called %d times, want 2 (no retry loop on bad credentials)", got)
	}
}

func TestGetJSONDoesNotRetryClientErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"not found", http.StatusNotFound},
		{"forbidden", http.StatusForbidden},
		{"bad request", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
				w.WriteHeader(tt.status)
			})
			c, slept := fk.client(t)

			err := c.getJSON(context.Background(), fk.adminURL("/groups/g1/children"), nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			if fk.adminCalls.Load() != 1 {
				t.Errorf("admin called %d times, want 1 (no retry)", fk.adminCalls.Load())
			}
			if len(*slept) != 0 {
				t.Errorf("should not back off, slept %v", *slept)
			}

			switch tt.status {
			case http.StatusNotFound:
				if !IsNotFound(err) {
					t.Error("IsNotFound() should be true — the /children capability probe depends on it")
				}
			case http.StatusForbidden:
				if !IsForbidden(err) {
					t.Error("IsForbidden() should be true")
				}
			}
		})
	}
}

func TestGetJSONMalformedBody(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		fmt.Fprint(w, `<html>not json</html>`)
	})
	c, _ := fk.client(t)

	var out struct{ Count int }
	err := c.getJSON(context.Background(), fk.adminURL("/users/count"), &out)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("error = %v, want a decode failure naming the URL", err)
	}
}

func TestGetJSONRespectsConcurrencyLimit(t *testing.T) {
	const limit = 3
	var inFlight, maxSeen atomic.Int32

	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		cur := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if cur <= old || maxSeen.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		inFlight.Add(-1)
		fmt.Fprint(w, `{}`)
	})
	c, _ := fk.client(t, func(cfg *Config) { cfg.Concurrency = limit })

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.getJSON(context.Background(), fk.adminURL("/users"), nil)
		}()
	}
	wg.Wait()

	if got := maxSeen.Load(); got > limit {
		t.Errorf("observed %d concurrent requests, limit is %d — a customer's Keycloak must not be hammered", got, limit)
	}
	if maxSeen.Load() < 2 {
		t.Log("warning: concurrency never exceeded 1; the test may not be exercising the limiter")
	}
}

func TestGetJSONStopsWhenContextCancelledDuringBackoff(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c, _ := fk.client(t)

	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}

	err := c.getJSON(ctx, fk.adminURL("/users"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", err)
	}
	// The original failure must survive in the message for diagnosis.
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q should mention the underlying failure", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "12", 12 * time.Second},
		{"zero", "0", 0},
		{"negative", "-5", 0},
		{"http date in the future", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"http date in the past", now.Add(-90 * time.Second).Format(http.TimeFormat), 0},
		{"garbage", "soon please", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryAfter(tt.header, now); got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestIsEndpointAbsent(t *testing.T) {
	// Capability detection depends on this classification being right: a 403 is
	// a missing role and must NOT be mistaken for an old server.
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusNotFound, true},
		{http.StatusMethodNotAllowed, true}, // Keycloak 22 for GET /groups/{id}/children
		{http.StatusForbidden, false},
		{http.StatusUnauthorized, false},
		{http.StatusInternalServerError, false},
	}

	for _, tt := range tests {
		err := error(&APIError{StatusCode: tt.status, Status: fmt.Sprint(tt.status)})
		if got := IsEndpointAbsent(err); got != tt.want {
			t.Errorf("IsEndpointAbsent(%d) = %v, want %v", tt.status, got, tt.want)
		}
	}

	if IsEndpointAbsent(errors.New("plain error")) {
		t.Error("a non-API error must not count as endpoint absence")
	}
}

func TestSleepCtx(t *testing.T) {
	t.Run("waits for the duration", func(t *testing.T) {
		start := time.Now()
		if err := sleepCtx(context.Background(), 30*time.Millisecond); err != nil {
			t.Fatalf("sleepCtx() error = %v", err)
		}
		if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
			t.Errorf("returned after %v, want at least ~30ms", elapsed)
		}
	})

	t.Run("returns early when the context is cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		if err := sleepCtx(ctx, 10*time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("took %v to notice cancellation; a Ctrl-C must not wait out the backoff", elapsed)
		}
	})

	t.Run("zero duration does not block", func(t *testing.T) {
		if err := sleepCtx(context.Background(), 0); err != nil {
			t.Errorf("error = %v, want nil for a zero delay", err)
		}
	})
}

func TestDefaultJitter(t *testing.T) {
	const base = 400 * time.Millisecond

	// Jitter must only ever extend the delay, never shorten it below the
	// exponential floor, and must stay within a predictable band.
	var sawVariation bool
	first := defaultJitter(base)
	for range 200 {
		got := defaultJitter(base)
		if got < base {
			t.Fatalf("jitter produced %v, shorter than the base %v", got, base)
		}
		if got > base+base/2 {
			t.Fatalf("jitter produced %v, beyond base+50%% (%v)", got, base+base/2)
		}
		if got != first {
			sawVariation = true
		}
	}
	if !sawVariation {
		t.Error("jitter never varied; concurrent workers would resynchronise into a thundering herd")
	}

	if got := defaultJitter(0); got != 0 {
		t.Errorf("defaultJitter(0) = %v, want 0", got)
	}
}

func TestGetJSONRetriesNetworkErrors(t *testing.T) {
	// A connection failure is transient in a way a status code is not: the
	// request never reached Keycloak, so it is always worth retrying. This path
	// is invisible to the status-code tests above.
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		fmt.Fprint(w, `{"ok":true}`)
	})
	c, slept := fk.client(t, func(cfg *Config) { cfg.MaxRetries = 2 })

	// Point at a port nothing is listening on, reusing the stub only for tokens.
	dead := "http://127.0.0.1:1/admin/realms/test/users"

	err := c.getJSON(context.Background(), dead, nil)
	if err == nil {
		t.Fatal("expected a connection failure")
	}

	var transportErr *transportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("error %v is not a *transportError", err)
	}
	if len(*slept) != 2 {
		t.Errorf("backed off %d times, want 2 (network errors must be retried)", len(*slept))
	}
	if !strings.Contains(err.Error(), dead) {
		t.Errorf("error %q should name the URL that failed", err)
	}
}

func TestTransportErrorUnwraps(t *testing.T) {
	inner := errors.New("connection refused")
	err := error(&transportError{URL: "http://kc/admin", err: inner})

	if !errors.Is(err, inner) {
		t.Error("transportError must unwrap to the underlying cause")
	}
	if !strings.Contains(err.Error(), "http://kc/admin") {
		t.Errorf("error %q should name the URL", err)
	}
}
