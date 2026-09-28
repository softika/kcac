package kc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeKeycloak is a stub Keycloak that serves the token endpoint itself and
// delegates every admin path to the handler under test.
type fakeKeycloak struct {
	srv        *httptest.Server
	adminCalls atomic.Int32
	tokenCalls atomic.Int32
	tokenTTL   int
	handler    func(w http.ResponseWriter, r *http.Request, call int32)
}

// newFakeKeycloak starts a stub server. handler receives a 1-based call counter
// so tests can fail the first N attempts and then succeed.
func newFakeKeycloak(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, call int32)) *fakeKeycloak {
	t.Helper()
	fk := &fakeKeycloak{tokenTTL: 3600, handler: handler}

	fk.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token") {
			n := fk.tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":%d}`, n, fk.tokenTTL)
			return
		}
		n := fk.adminCalls.Add(1)
		fk.handler(w, r, n)
	}))
	t.Cleanup(fk.srv.Close)
	return fk
}

// client builds a Client pointed at the stub with instant, recorded backoff so
// retry logic is verified without real delays.
func (fk *fakeKeycloak) client(t *testing.T, tune ...func(*Config)) (*Client, *[]time.Duration) {
	t.Helper()
	cfg := Config{
		BaseURL:      fk.srv.URL,
		Realm:        "test",
		ClientID:     "kcac-audit",
		ClientSecret: "s3cret",
	}
	for _, f := range tune {
		f(&cfg)
	}

	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	c.httpClient = fk.srv.Client()
	c.tokens = newTokenManager(c.cfg, fk.srv.Client())

	var slept []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return ctx.Err()
	}
	c.jitter = func(d time.Duration) time.Duration { return d } // deterministic
	return c, &slept
}

func (fk *fakeKeycloak) adminURL(path string) string {
	return fk.srv.URL + "/admin/realms/test" + path
}
