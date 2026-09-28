package kc

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// maxPages bounds a pagination loop. A server that ignores the first parameter
// would otherwise spin forever; failing loudly beats hanging on a customer's
// realm with no output.
const maxPages = 10_000

// paged fetches every page of a paginated Admin API collection.
//
// kcac always sends an explicit max. Server-side defaults differ per endpoint —
// 100 for /users but only 10 for /groups/{id}/children — so relying on them
// silently truncates results, which in an access review means missing
// entitlements presented as a complete picture.
func paged[T any](ctx context.Context, c *Client, baseURL string, extra url.Values) ([]T, error) {
	var all []T
	err := pagedEach(ctx, c, baseURL, extra, func(batch []T) bool {
		all = append(all, batch...)
		return true
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

// pagedEach fetches every page and hands each batch to fn as it arrives.
//
// Returning false from fn stops paging. This exists so a caller that only needs
// a summary can fold each page away and keep memory proportional to the summary
// rather than to the whole collection, which matters for the event log: a busy
// realm holds millions of events and accumulating them first defeats the point
// of having a cap.
func pagedEach[T any](ctx context.Context, c *Client, baseURL string, extra url.Values, fn func([]T) bool) error {
	pageSize := c.cfg.PageSize

	for page, first := 0, 0; ; page++ {
		if page >= maxPages {
			return fmt.Errorf("pagination of %s exceeded %d pages: the server appears to ignore the 'first' parameter", baseURL, maxPages)
		}

		params := url.Values{}
		for k, vs := range extra {
			params[k] = vs
		}
		params.Set("first", strconv.Itoa(first))
		params.Set("max", strconv.Itoa(pageSize))

		var batch []T
		if err := c.getJSON(ctx, query(baseURL, params), &batch); err != nil {
			return err
		}

		if !fn(batch) {
			return nil
		}

		// A short page is the last page. An empty page ends the loop even if the
		// server returned a full page previously.
		if len(batch) < pageSize {
			return nil
		}
		first += len(batch)
	}
}

// briefFalse is sent on every endpoint that supports it.
//
// briefRepresentation defaults to TRUE on /groups and /users/{id}/groups, which
// returns little more than name and id. A caller that trusts the default loses
// fields it never knew it asked for — one of the quiet ways a hand-rolled export
// produces incomplete evidence.
func briefFalse() url.Values {
	return url.Values{"briefRepresentation": {"false"}}
}
