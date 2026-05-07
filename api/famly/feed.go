package famly

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// FeedPageSize is the page size we ask Famly for. The vendor's
// "first" parameter is advisory; observed page sizes are smaller
// than requested under load.
const FeedPageSize = 10

// Pages returns an iterator over feed pages, starting from the most
// recent and walking backwards. Each yield is (page, error). On
// error, the iterator yields once with the error and stops.
//
// The iterator stops when the vendor returns no more items or when
// the consumer's yield returns false (e.g. on context cancellation
// or max-pages limit).
//
// Pagination uses (cursor, olderThan) carried from the last item of
// the previous page, matching the official client's behaviour.
func (c *Client) Pages(ctx context.Context) iter.Seq2[FeedPage, error] {
	return func(yield func(FeedPage, error) bool) {
		var (
			cursor    string
			olderThan time.Time
		)
		for {
			page, err := c.feedPage(ctx, cursor, olderThan, FeedPageSize)
			if err != nil {
				yield(FeedPage{}, err)
				return
			}
			if !yield(page, nil) {
				return
			}
			if len(page.FeedItems) == 0 {
				return
			}
			last := page.FeedItems[len(page.FeedItems)-1]
			cursor = last.FeedItemID
			olderThan = last.CreatedDate.Time
		}
	}
}

// feedPage fetches a single page. cursor and olderThan are zero
// values for the first call.
func (c *Client) feedPage(ctx context.Context, cursor string, olderThan time.Time, limit int) (FeedPage, error) {
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if !olderThan.IsZero() {
		q.Set("olderThan", olderThan.Format(time.RFC3339))
	}
	if limit > 0 {
		q.Set("first", strconv.Itoa(limit))
	}
	var out FeedPage
	if err := c.do(ctx, http.MethodGet, "/api/feed/feed/feed", q, nil, &out); err != nil {
		if errors.Is(err, ErrUnauthorized) {
			c.tokenSrc.Invalidate()
		}
		return FeedPage{}, err
	}
	return out, nil
}
