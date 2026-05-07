package retry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
)

func TestDoSucceedsOnFirstTry(t *testing.T) {
	calls := 0
	v, err := Do(context.Background(), func() (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if v != "ok" || calls != 1 {
		t.Errorf("v=%q calls=%d", v, calls)
	}
}

func TestDoRetriesTransient(t *testing.T) {
	var calls atomic.Int32
	v, err := Do(context.Background(), func() (string, error) {
		n := calls.Add(1)
		if n < 3 {
			return "", errors.New("transient")
		}
		return "ok", nil
	}, backoff.WithBackOff(&backoff.ZeroBackOff{}))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if v != "ok" || calls.Load() != 3 {
		t.Errorf("v=%q calls=%d", v, calls.Load())
	}
}

func TestDoStopsOnPermanent(t *testing.T) {
	calls := 0
	_, err := Do(context.Background(), func() (string, error) {
		calls++
		return "", backoff.Permanent(errors.New("nope"))
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("permanent should not retry; calls=%d", calls)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should preserve the underlying message: %v", err)
	}
}

func TestHTTPDoRetries5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	resp, err := HTTPDo(context.Background(), func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	}, backoff.WithBackOff(&backoff.ZeroBackOff{}))
	if err != nil {
		t.Fatalf("HTTPDo: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || calls.Load() != 3 {
		t.Errorf("status=%d calls=%d", resp.StatusCode, calls.Load())
	}
}

func TestHTTPDoStopsOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)

	_, err := HTTPDo(context.Background(), func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention HTTP 400: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("4xx should not retry; calls=%d", calls.Load())
	}
}

func TestHTTPDoHonoursRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	resp, err := HTTPDo(context.Background(), func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err != nil {
		t.Fatalf("HTTPDo: %v", err)
	}
	defer resp.Body.Close()
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("expected at least 1s delay from Retry-After, got %v", elapsed)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("3"); got != 3 {
		t.Errorf("seconds: %d", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty: %d", got)
	}
	if got := parseRetryAfter("not-a-number"); got != 0 {
		t.Errorf("garbage: %d", got)
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got < 8 || got > 11 {
		t.Errorf("HTTP-date: %d (expected ~10)", got)
	}
}

func TestHTTPDoCancelsOnContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := HTTPDo(ctx, func(ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		return http.DefaultClient.Do(req)
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("expected deadline-exceeded-shaped error, got %v", err)
	}
}
