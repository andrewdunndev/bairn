package sync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/asset"
	"gitlab.com/dunn.dev/bairn/internal/retry"
	"gitlab.com/dunn.dev/bairn/internal/safehttp"
	"gitlab.com/dunn.dev/bairn/internal/sink"
)

// instantRetry retries without waiting.
func instantRetry(attempts int) retry.Policy {
	return retry.Policy{Attempts: attempts, Initial: time.Millisecond, Max: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil }}
}

// flakyCDN serves one image; the first fails calls hit /img/a (with
// reset: hang up mid-body, else 503), later calls and /img/b succeed.
// It records which path each call used.
func flakyCDN(t *testing.T, fails int32, reset bool) (feed *httptest.Server, hits *[]string, calls *atomic.Int32) {
	t.Helper()
	body := jpegBytes(t)
	var seen []string
	calls = &atomic.Int32{}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	feedCalls := &atomic.Int32{}
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("cursor") != "" {
			_, _ = w.Write([]byte(`{"feedItems": []}`))
			return
		}
		// Every read of the feed signs a new URL.
		n := feedCalls.Add(1)
		fmt.Fprintf(w, `{"feedItems":[{"feedItemId":"post-1","originatorId":"Post:e1","createdDate":"2026-05-06T14:00:00Z",
"images":[{"imageId":"img-1","url":"%[1]s/img/%[2]d","url_big":"%[1]s/img/%[2]d","width":0,"height":0,
"createdAt":{"date":"2026-05-06T14:00:00Z"},"liked":false,"likes":[],"tags":[]}],"videos":[]}]}`, srv.URL, n)
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		if calls.Add(1) <= fails {
			if reset {
				w.Header().Set("Content-Length", fmt.Sprint(len(body)+100))
				_, _ = w.Write(body[:len(body)/2])
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				panic(http.ErrAbortHandler)
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(body)
	})
	return srv, &seen, calls
}

func runFlaky(t *testing.T, srv *httptest.Server, attempts int) (Result, string) {
	t.Helper()
	saveDir := t.TempDir()
	disk, _ := sink.NewDisk(saveDir, "", "")
	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(srv.URL), famly.WithPageDelay(0))
	res, err := Run(context.Background(), Deps{
		Famly: fc, Disk: disk, State: openTestStore(t), DownloadRetry: instantRetry(attempts),
	}, Options{MaxPages: 1, Source: SourceAll})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, saveDir
}

func TestDownloadRetriesAResetAndUsesAFreshURL(t *testing.T) {
	srv, hits, calls := flakyCDN(t, 2, true)
	res, saveDir := runFlaky(t, srv, 4)
	if res.Saved != 1 || res.Errors != 0 || res.Buckets.SavedNew != 1 {
		t.Fatalf("want one clean save after retries, got %+v", res)
	}
	if calls.Load() != 3 {
		t.Errorf("CDN calls = %d, want 3 (two resets then success)", calls.Load())
	}
	// Each retry re-read the feed item and so got a newly signed URL.
	if len(*hits) != 3 || (*hits)[0] == (*hits)[1] || (*hits)[1] == (*hits)[2] {
		t.Errorf("retries reused a URL: %v", *hits)
	}
	// No partial file survives the failed attempts.
	left, _ := filepath.Glob(filepath.Join(saveDir, asset.TempPrefix+"*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
	if ents, _ := os.ReadDir(saveDir); len(ents) != 1 {
		t.Errorf("save dir holds %d entries, want only the day directory", len(ents))
	}
}

func TestDownloadRetriesA5xx(t *testing.T) {
	srv, _, calls := flakyCDN(t, 1, false)
	res, _ := runFlaky(t, srv, 4)
	if res.Saved != 1 || calls.Load() != 2 {
		t.Errorf("saved=%d calls=%d, want 1 saved over 2 calls", res.Saved, calls.Load())
	}
}

func TestDownloadStopsAtTheAttemptBound(t *testing.T) {
	srv, _, calls := flakyCDN(t, 100, false)
	res, _ := runFlaky(t, srv, 3)
	if calls.Load() != 3 {
		t.Errorf("CDN calls = %d, want exactly 3", calls.Load())
	}
	if res.Errors != 1 || res.Buckets.Failed != 1 || res.Saved != 0 {
		t.Errorf("want one failure, got %+v", res)
	}
}

func TestDownloadDoesNotRetryAPermanentStatus(t *testing.T) {
	// 403 is an expired signed URL: retrying it would only storm the CDN.
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) { permanentStatus(t, status) })
	}
}

func permanentStatus(t *testing.T, status int) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			_, _ = w.Write([]byte(`{"feedItems": []}`))
			return
		}
		fmt.Fprintf(w, `{"feedItems":[{"feedItemId":"p","createdDate":"2026-05-06T14:00:00Z","images":[{"imageId":"i","url":"%s/gone","url_big":"%s/gone","createdAt":{"date":"2026-05-06T14:00:00Z"}}],"videos":[]}]}`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
	})
	res, _ := runFlaky(t, srv, 4)
	if calls.Load() != 1 || res.Errors != 1 {
		t.Errorf("a %d must not be retried: calls=%d errors=%d", status, calls.Load(), res.Errors)
	}
}

func TestIsTransient(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("x: %w", &asset.HTTPStatusError{Status: 502}), true},
		{fmt.Errorf("x: %w", &asset.HTTPStatusError{Status: 404}), false},
		{fmt.Errorf("x: %w", &asset.HTTPStatusError{Status: 403}), false},
		{fmt.Errorf("x: %w", &asset.HTTPStatusError{Status: 429}), true},
		{fmt.Errorf("x: %w", &asset.HTTPStatusError{Status: 408}), true},
		{fmt.Errorf("x: %w", io.ErrUnexpectedEOF), true},
		{fmt.Errorf("x: %w", io.EOF), true},
		{fmt.Errorf("x: %w", safehttp.ErrIdle), true},
		{fmt.Errorf("read: connection reset by peer"), true},
		{fmt.Errorf("x: %w", context.Canceled), false},
		{fmt.Errorf("disk full"), false},
	} {
		if got := asset.IsTransient(tc.err); got != tc.want {
			t.Errorf("IsTransient(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	if !strings.Contains((&asset.HTTPStatusError{Status: 503}).Error(), "503") {
		t.Error("status error text lost the code")
	}
}
