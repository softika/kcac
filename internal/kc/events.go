package kc

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"time"
)

// maxLoginEvents bounds an events sweep. A busy realm can hold millions of
// events; kcac reads a bounded window and says so rather than exhausting memory
// on a customer's machine.
const maxLoginEvents = 200_000

// EventSettings describes whether the realm records logins at all.
//
// This distinction is the reason kcac queries it: an empty events response from
// a realm with logging disabled looks exactly like an empty response from a realm
// where nobody logged in. Reporting "never logged in" from the first case is a
// revocation recommendation built on no evidence.
type EventSettings struct {
	Enabled bool
	// Expiration is the retention period in seconds; 0 means events are kept
	// until manually cleared.
	Expiration int64
	// EnabledTypes lists the recorded event types. Empty means all types.
	EnabledTypes []string
}

// RecordsLogins reports whether LOGIN events are actually being written, and why
// not when they are not.
func (s EventSettings) RecordsLogins() (bool, string) {
	if !s.Enabled {
		return false, "event logging is disabled on this realm (Realm settings → Sessions and events)"
	}
	if len(s.EnabledTypes) > 0 && !slices.Contains(s.EnabledTypes, "LOGIN") {
		return false, "the realm records events but LOGIN is not among the enabled event types"
	}
	return true, ""
}

// EventSettings reads the realm's event configuration.
func (c *Client) EventSettings(ctx context.Context) (EventSettings, error) {
	var realm struct {
		EventsEnabled     bool     `json:"eventsEnabled"`
		EventsExpiration  int64    `json:"eventsExpiration"`
		EnabledEventTypes []string `json:"enabledEventTypes"`
	}
	if err := c.getJSON(ctx, c.cfg.adminPath(""), &realm); err != nil {
		return EventSettings{}, fmt.Errorf("read realm event settings: %w", err)
	}
	return EventSettings{
		Enabled:      realm.EventsEnabled,
		Expiration:   realm.EventsExpiration,
		EnabledTypes: realm.EnabledEventTypes,
	}, nil
}

// loginEvent is the subset of an event representation kcac needs.
type loginEvent struct {
	Time   int64  `json:"time"` // epoch milliseconds
	Type   string `json:"type"`
	UserID string `json:"userId"`
}

// LastLogins returns the most recent LOGIN per user within the window starting
// at since.
//
// A user absent from the result had no login recorded in that window. That is
// not the same as never having logged in — they may have logged in before the
// window opened, or before retention expired — and callers must not present it
// as such.
func (c *Client) LastLogins(ctx context.Context, since time.Time) (map[string]time.Time, []string, error) {
	params := url.Values{
		"type": {"LOGIN"},
		// Keycloak's events endpoint takes a plain date here across every major
		// kcac supports.
		"dateFrom": {since.UTC().Format("2006-01-02")},
	}

	// Each page is folded into the result and then discarded, so peak memory is
	// proportional to the number of accounts seen, not to the number of events.
	// Accumulating first and trimming afterwards would let a busy realm allocate
	// hundreds of megabytes before any cap took effect.
	var (
		latest   = make(map[string]time.Time)
		scanned  int
		capped   bool
		warnings []string
	)

	err := pagedEach(ctx, c, c.cfg.adminPath("/events"), params, func(batch []loginEvent) bool {
		for _, e := range batch {
			if scanned >= maxLoginEvents {
				capped = true
				return false
			}
			scanned++

			if e.UserID == "" || e.Time == 0 {
				continue
			}
			when := time.UnixMilli(e.Time).UTC()
			if prev, ok := latest[e.UserID]; !ok || when.After(prev) {
				latest[e.UserID] = when
			}
		}
		return true
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read login events: %w", err)
	}

	if capped {
		warnings = append(warnings, fmt.Sprintf(
			"stopped after %d login events; last_login may be older than reality for some accounts, so narrow --events-window",
			maxLoginEvents))
	}
	return latest, warnings, nil
}
