package retry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fake returns a Policy whose sleeps are recorded, not waited.
func fake(waits *[]time.Duration) Policy {
	p := Default()
	p.Sleep = func(_ context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return nil
	}
	return p
}

func get(srv *httptest.Server) func() (*http.Response, error) {
	return func() (*http.Response, error) { return http.Get(srv.URL) }
}

func TestRetriesServerErrorThenSucceeds(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	var waits []time.Duration
	resp, err := fake(&waits).Do(context.Background(), get(srv))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || n.Load() != 3 || len(waits) != 2 {
		t.Fatalf("status=%d calls=%d waits=%v", resp.StatusCode, n.Load(), waits)
	}
	if waits[1] < waits[0] {
		t.Errorf("backoff should grow: %v", waits)
	}
}

func TestNeverRetriesOtherClientErrors(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 422} {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			w.WriteHeader(code)
		}))
		var waits []time.Duration
		resp, err := fake(&waits).Do(context.Background(), get(srv))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		srv.Close()
		if resp.StatusCode != code || n.Load() != 1 {
			t.Errorf("%d: status=%d calls=%d", code, resp.StatusCode, n.Load())
		}
	}
}

func TestHonoursRetryAfterOn429(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer srv.Close()
	var waits []time.Duration
	resp, err := fake(&waits).Do(context.Background(), get(srv))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(waits) != 1 || waits[0] != 7*time.Second {
		t.Fatalf("waits=%v, want [7s]", waits)
	}
}

func TestRetryAfterBeyondMaxIsHonoured(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer srv.Close()
	var waits []time.Duration
	p := fake(&waits)
	resp, err := p.Do(context.Background(), get(srv))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(waits) != 1 || waits[0] != 120*time.Second {
		t.Fatalf("waits=%v, want one 120s wait", waits)
	}
}

func TestRetryAfterPastCeilingReturnsAtOnce(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", strconv.Itoa(3600))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	var waits []time.Duration
	resp, err := fake(&waits).Do(context.Background(), get(srv))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || n.Load() != 1 || len(waits) != 0 {
		t.Fatalf("status=%d tries=%d waits=%v, want 429 after one try", resp.StatusCode, n.Load(), waits)
	}
}

func TestRetriesTransportError(t *testing.T) {
	var n int
	var waits []time.Duration
	resp, err := fake(&waits).Do(context.Background(), func() (*http.Response, error) {
		n++
		if n < 3 {
			return nil, errors.New("connection reset")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if n != 3 {
		t.Fatalf("calls=%d", n)
	}
}

func TestExhaustionReturnsLastResponse(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "boom")
	}))
	defer srv.Close()
	var waits []time.Duration
	p := fake(&waits)
	resp, err := p.Do(context.Background(), get(srv))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || string(b) != "boom" || int(n.Load()) != p.Attempts {
		t.Fatalf("status=%d body=%q calls=%d", resp.StatusCode, b, n.Load())
	}
}

func TestExhaustionReturnsTransportError(t *testing.T) {
	var waits []time.Duration
	p := fake(&waits)
	want := errors.New("dial refused")
	_, err := p.Do(context.Background(), func() (*http.Response, error) { return nil, want })
	if !errors.Is(err, want) || len(waits) != p.Attempts-1 {
		t.Fatalf("err=%v waits=%d", err, len(waits))
	}
}

func TestContextCancelStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := Default()
	p.Sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	var n int
	_, err := p.Do(ctx, func() (*http.Response, error) { n++; return nil, errors.New("down") })
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("err=%v calls=%d", err, n)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if d, ok := retryAfter("3"); !ok || d != 3*time.Second {
		t.Errorf("seconds: %v %v", d, ok)
	}
	date := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	if d, ok := retryAfter(date); !ok || d < 59*time.Minute {
		t.Errorf("date: %v %v", d, ok)
	}
	if _, ok := retryAfter("soon"); ok {
		t.Error("garbage should not parse")
	}
}
