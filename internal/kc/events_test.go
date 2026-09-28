package kc

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEventSettingsRecordsLogins(t *testing.T) {
	tests := []struct {
		name       string
		settings   EventSettings
		wantOK     bool
		wantReason string
	}{
		{
			name:     "events on, all types",
			settings: EventSettings{Enabled: true},
			wantOK:   true,
		},
		{
			name:     "events on, LOGIN explicitly enabled",
			settings: EventSettings{Enabled: true, EnabledTypes: []string{"LOGIN", "LOGOUT"}},
			wantOK:   true,
		},
		{
			name:       "events disabled entirely",
			settings:   EventSettings{},
			wantOK:     false,
			wantReason: "disabled on this realm",
		},
		{
			name:       "events on but LOGIN not recorded",
			settings:   EventSettings{Enabled: true, EnabledTypes: []string{"LOGOUT", "CLIENT_LOGIN"}},
			wantOK:     false,
			wantReason: "LOGIN is not among the enabled event types",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := tt.settings.RecordsLogins()
			if ok != tt.wantOK {
				t.Errorf("RecordsLogins() ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantReason != "" && !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to mention %q", reason, tt.wantReason)
			}
		})
	}
}

func TestEventSettingsReadsRealm(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		fmt.Fprint(w, `{"eventsEnabled":true,"eventsExpiration":604800,"enabledEventTypes":["LOGIN"]}`)
	})
	c, _ := fk.client(t)

	got, err := c.EventSettings(context.Background())
	if err != nil {
		t.Fatalf("EventSettings() error = %v", err)
	}
	if !got.Enabled || got.Expiration != 604800 {
		t.Errorf("settings = %+v", got)
	}
}

func TestLastLoginsKeepsTheMostRecentPerUser(t *testing.T) {
	older := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 20, 14, 3, 11, 0, time.UTC)

	var gotType, gotDateFrom string
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, call int32) {
		gotType = r.URL.Query().Get("type")
		gotDateFrom = r.URL.Query().Get("dateFrom")
		if call > 1 {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `[
		  {"time":%d,"type":"LOGIN","userId":"u1"},
		  {"time":%d,"type":"LOGIN","userId":"u1"},
		  {"time":%d,"type":"LOGIN","userId":"u2"},
		  {"time":0,"type":"LOGIN","userId":"u3"},
		  {"time":%d,"type":"LOGIN","userId":""}
		]`, older.UnixMilli(), newer.UnixMilli(), older.UnixMilli(), newer.UnixMilli())
	})
	c, _ := fk.client(t)

	got, warnings, err := c.LastLogins(context.Background(), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("LastLogins() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}

	if gotType != "LOGIN" {
		t.Errorf("type filter = %q, want LOGIN", gotType)
	}
	if gotDateFrom != "2026-07-01" {
		t.Errorf("dateFrom = %q, want 2026-07-01", gotDateFrom)
	}

	if !got["u1"].Equal(newer) {
		t.Errorf("u1 = %v, want the most recent login %v", got["u1"], newer)
	}
	if !got["u2"].Equal(older) {
		t.Errorf("u2 = %v, want %v", got["u2"], older)
	}
	// Records without a usable timestamp or user are skipped rather than
	// producing a bogus epoch-zero login.
	if _, ok := got["u3"]; ok {
		t.Errorf("u3 has a zero timestamp and should be skipped, got %v", got["u3"])
	}
	if _, ok := got[""]; ok {
		t.Error("an event with no userId should be skipped")
	}
}

func TestLastLoginsEmptyWhenNothingRecorded(t *testing.T) {
	fk := newFakeKeycloak(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		fmt.Fprint(w, `[]`)
	})
	c, _ := fk.client(t)

	got, _, err := c.LastLogins(context.Background(), time.Now().Add(-90*24*time.Hour))
	if err != nil {
		t.Fatalf("LastLogins() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d logins, want none", len(got))
	}
}

func TestLastLoginsStopsAtTheCapWithoutBufferingEverything(t *testing.T) {
	// The cap has to stop the sweep, not trim an already-accumulated slice.
	// Otherwise the real ceiling is maxPages * PageSize events held in memory.
	var pagesServed atomic.Int32

	fk := newFakeKeycloak(t, func(w http.ResponseWriter, r *http.Request, _ int32) {
		pagesServed.Add(1)
		max := queryIntValue(r, "max")
		first := queryIntValue(r, "first")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("["))
		for i := range max {
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = fmt.Fprintf(w, `{"time":%d,"type":"LOGIN","userId":"u%d"}`,
				time.Now().UnixMilli(), first+i)
		}
		_, _ = w.Write([]byte("]"))
	})

	// PageSize 1000 with an endless server: without an early stop this pages
	// until maxPages and holds every event.
	c, _ := fk.client(t, func(cfg *Config) { cfg.PageSize = 1000 })

	got, warnings, err := c.LastLogins(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("LastLogins() error = %v", err)
	}

	// Stopping at the cap means about maxLoginEvents/PageSize pages, far short of
	// the maxPages ceiling.
	wantPages := int32(maxLoginEvents/1000) + 1
	if pagesServed.Load() > wantPages {
		t.Errorf("served %d pages, want no more than %d — the sweep did not stop at the cap",
			pagesServed.Load(), wantPages)
	}
	if len(got) > maxLoginEvents {
		t.Errorf("collected %d accounts, more than the %d event cap", len(got), maxLoginEvents)
	}
	if len(warnings) == 0 {
		t.Error("hitting the cap must be reported, or last_login looks more complete than it is")
	}
}

func queryIntValue(r *http.Request, key string) int {
	var n int
	_, _ = fmt.Sscanf(r.URL.Query().Get(key), "%d", &n)
	return n
}
