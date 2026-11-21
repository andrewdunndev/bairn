package famly

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/retry"
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
	return c.PagesFrom(ctx, time.Time{})
}

// PagesFrom is Pages starting at the feed as it stood at start: items
// created at or before start, newest first. A zero start begins at the
// most recent item. The jump uses the same olderThan parameter the
// pagination does, without a cursor; a server that ignored it would
// return the newest page instead, which callers tolerate by checking
// the dates they get back.
func (c *Client) PagesFrom(ctx context.Context, start time.Time) iter.Seq2[FeedPage, error] {
	return func(yield func(FeedPage, error) bool) {
		var cursor string
		olderThan := start
		if !start.IsZero() {
			// olderThan is exclusive, so reach one second past start.
			olderThan = start.Add(time.Second)
		}
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
			// A jumped first request carries no cursor and is not held
			// to its olderThan: a server that ignored it is tolerated.
			if last.FeedItemID == cursor || (cursor != "" && last.CreatedDate.After(olderThan)) {
				yield(FeedPage{}, errors.New("famly: feed cursor did not advance"))
				return
			}
			cursor = last.FeedItemID
			olderThan = last.CreatedDate.Time
			if err := retry.Sleep(ctx, c.pageDelay); err != nil {
				yield(FeedPage{}, err)
				return
			}
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
		return FeedPage{}, err
	}
	return out, nil
}

// ErrItemNotFound is returned by FindItem when the feed does not hold
// the item near its date.
var ErrItemNotFound = errors.New("famly: feed item not found near its date")

// findItemPages bounds how far FindItem reads past the item's date.
const findItemPages = 3

// FindItem re-reads one feed item, with fresh signed media URLs, by
// jumping the feed to its creation date (see PagesFrom) and scanning
// at most findItemPages pages. Famly exposes no per-item lookup.
func (c *Client) FindItem(ctx context.Context, feedItemID string, createdAt time.Time) (FeedItem, error) {
	if feedItemID == "" || createdAt.IsZero() {
		return FeedItem{}, ErrItemNotFound
	}
	pages := 0
	for page, err := range c.PagesFrom(ctx, createdAt) {
		if err != nil {
			return FeedItem{}, err
		}
		for _, it := range page.FeedItems {
			if it.FeedItemID == feedItemID {
				return it, nil
			}
		}
		if pages++; pages >= findItemPages {
			break
		}
	}
	return FeedItem{}, ErrItemNotFound
}
