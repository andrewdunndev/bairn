package famly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPagesIterator(t *testing.T) {
	mux := http.NewServeMux()

	// Page 1 returns 2 items; page 2 returns empty (signals end).
	page1 := fixture(t, "feed-page1.json")
	page2 := fixture(t, "feed-page2.json")

	calls := 0
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(authHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			// First call: no cursor, return page1.
			if got := r.URL.Query().Get("cursor"); got != "" {
				t.Errorf("first call: cursor = %q, want empty", got)
			}
			_, _ = w.Write(page1)
			return
		}
		// Second call: cursor should be the last feedItemId of page1.
		if got, want := r.URL.Query().Get("cursor"), "post-002"; got != want {
			t.Errorf("second call: cursor = %q, want %q", got, want)
		}
		_, _ = w.Write(page2)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(NewStaticToken("test-token"), WithBaseURL(srv.URL))

	var pages int
	var totalItems int
	for page, err := range c.Pages(context.Background()) {
		if err != nil {
			t.Fatalf("Pages yielded error: %v", err)
		}
		pages++
		totalItems += len(page.FeedItems)
		if pages > 5 {
			t.Fatal("paginator did not stop on empty page")
		}
	}
	if pages != 2 {
		t.Errorf("pages = %d, want 2", pages)
	}
	if totalItems != 2 {
		t.Errorf("totalItems = %d, want 2", totalItems)
	}
}

func TestPagesEarlyStop(t *testing.T) {
	mux := http.NewServeMux()
	page1 := fixture(t, "feed-page1.json")
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(page1)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := New(NewStaticToken("test-token"), WithBaseURL(srv.URL))
	pages := 0
	for range c.Pages(context.Background()) {
		pages++
		if pages >= 1 {
			break // exercise early consumer-driven stop
		}
	}
	if pages != 1 {
		t.Errorf("early-stop: pages = %d, want 1", pages)
	}
}
