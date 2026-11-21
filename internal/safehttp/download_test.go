package safehttp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadClientStalledBodyFailsFast(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := newDownloadClient(time.Second, time.Second, time.Second, 200*time.Millisecond)
	start := time.Now()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, ErrIdle) {
		t.Fatalf("err = %v, want ErrIdle", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v, want fast failure", d)
	}
}

func TestDownloadClientStalledHeadersFailFast(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := newDownloadClient(time.Second, time.Second, 200*time.Millisecond, 5*time.Second)
	start := time.Now()
	if _, err := c.Get(srv.URL); err == nil {
		t.Fatal("expected a timeout error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v, want fast failure", d)
	}
}

func TestDownloadClientSlowProgressSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 6; i++ {
			_, _ = w.Write([]byte("0123456789"))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()

	// Total time (~600ms) exceeds the idle window (300ms); progress keeps it alive.
	c := newDownloadClient(time.Second, time.Second, time.Second, 300*time.Millisecond)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || len(b) != 60 {
		t.Fatalf("read %d bytes, err %v", len(b), err)
	}
}

func TestDownloadClientRefusesCrossHostRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "x")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(other.URL, "127.0.0.1", "localhost", 1), http.StatusFound)
	}))
	defer srv.Close()
	if _, err := NewDownloadClient().Get(srv.URL); err == nil {
		t.Fatal("expected cross-host redirect refusal")
	}
}
