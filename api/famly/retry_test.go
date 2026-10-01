package famly

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/retry"
)

func fastRetry() Option {
	return WithRetry(retry.Policy{Attempts: 3, Initial: time.Millisecond, Max: 5 * time.Millisecond})
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestRetriesServerErrorThenSucceeds(t *testing.T) {
	var n atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	c := New(NewStaticToken("t"), WithBaseURL(srv.URL), fastRetry())
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if n.Load() != 3 {
		t.Errorf("calls = %d, want 3", n.Load())
	}
}

func TestServerErrorGivesUpAfterAttempts(t *testing.T) {
	var n atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := New(NewStaticToken("t"), WithBaseURL(srv.URL), fastRetry())
	_, err := c.Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v, want HTTP 500", err)
	}
	if n.Load() != 3 {
		t.Errorf("calls = %d, want 3", n.Load())
	}
}

func TestOtherClientErrorsNotRetried(t *testing.T) {
	var n atomic.Int32
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	c := New(NewStaticToken("t"), WithBaseURL(srv.URL), fastRetry())
	if _, err := c.Me(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if n.Load() != 1 {
		t.Errorf("calls = %d, want 1", n.Load())
	}
}

func TestUnauthorizedRefreshesOnceAndRetries(t *testing.T) {
	var logins atomic.Int32
	src := NewRefreshingToken("a@example.com", "pw", "dev",
		func(ctx context.Context, email, password, deviceID string) (string, error) {
			if logins.Add(1) == 1 {
				return "stale", nil
			}
			return "fresh", nil
		})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(authHeader) != "fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	c := New(src, WithBaseURL(srv.URL), fastRetry())
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatalf("Me: %v", err)
	}
	if logins.Load() != 2 {
		t.Errorf("logins = %d, want 2", logins.Load())
	}
}

func TestSecondUnauthorizedFails(t *testing.T) {
	var reqs, logins atomic.Int32
	src := NewRefreshingToken("a@example.com", "pw", "dev",
		func(ctx context.Context, email, password, deviceID string) (string, error) {
			logins.Add(1)
			return "tok", nil
		})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := New(src, WithBaseURL(srv.URL), fastRetry())
	_, err := c.Me(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if reqs.Load() != 2 || logins.Load() != 2 {
		t.Errorf("requests = %d logins = %d, want 2 and 2", reqs.Load(), logins.Load())
	}
}

func TestPagesPauseBetweenPages(t *testing.T) {
	page1 := fixture(t, "feed-page1.json")
	page2 := fixture(t, "feed-page2.json")
	var stamps []time.Time
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		stamps = append(stamps, time.Now())
		if len(stamps) == 1 {
			_, _ = w.Write(page1)
			return
		}
		_, _ = w.Write(page2)
	})
	const delay = 80 * time.Millisecond
	c := New(NewStaticToken("t"), WithBaseURL(srv.URL), WithPageDelay(delay))
	for _, err := range c.Pages(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(stamps) != 2 {
		t.Fatalf("requests = %d, want 2", len(stamps))
	}
	if gap := stamps[1].Sub(stamps[0]); gap < delay {
		t.Errorf("gap between pages = %v, want >= %v", gap, delay)
	}
}

func TestDefaultPageDelayIsOneSecond(t *testing.T) {
	if c := New(NewStaticToken("t")); c.pageDelay != time.Second {
		t.Errorf("default page delay = %v, want 1s", c.pageDelay)
	}
}
