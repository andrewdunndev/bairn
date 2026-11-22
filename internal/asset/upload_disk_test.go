package asset

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

func TestUploadFromDiskRefusesPathOutsideRoot(t *testing.T) {
	ctx := context.Background()
	st, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Discover(ctx, "x", state.Asset{Source: "feed-image"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSaved(ctx, "x", "/etc/hosts", time.Now()); err != nil {
		t.Fatal(err)
	}
	// An unreachable Immich: the refusal must come before any request.
	im := sink.NewImmich(immich.New("http://127.0.0.1:1", "k"))
	_, err = UploadFromDisk(ctx, im, st, t.TempDir(), "x")
	if err == nil || !strings.Contains(err.Error(), "outside the save directory") {
		t.Fatalf("err = %v, want refusal outside the save directory", err)
	}
}

// diskFixture saves a media file under a fresh root and records it as
// saved-not-uploaded, the state UploadFromDisk recovers.
func diskFixture(t *testing.T, body []byte) (*state.Store, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	st, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := filepath.Join(root, "x.jpg")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Discover(ctx, "x", state.Asset{Source: "feed-image"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSaved(ctx, "x", p, time.Now()); err != nil {
		t.Fatal(err)
	}
	return st, root
}

func TestUploadFromDiskRecordsChecksumOfSentBytes(t *testing.T) {
	for _, status := range []string{"created", "duplicate"} {
		t.Run(status, func(t *testing.T) {
			var received []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Errorf("parse: %v", err)
					return
				}
				for _, fhs := range r.MultipartForm.File {
					f, _ := fhs[0].Open()
					received, _ = io.ReadAll(f)
					_ = f.Close()
					break
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"id":"a-1","status":%q}`, status)
			}))
			t.Cleanup(srv.Close)
			st, root := diskFixture(t, makeJPEG(t))
			ctx := context.Background()
			got, err := UploadFromDisk(ctx, sink.NewImmich(immich.New(srv.URL, "k")), st, root, "x")
			if err != nil || got != status {
				t.Fatalf("UploadFromDisk = %q, %v", got, err)
			}
			a, _ := st.Get(ctx, "x")
			if len(received) == 0 || a.UploadSHA1 != sumHex(received) {
				t.Errorf("UploadSHA1 = %q, want sha1 of received bytes %q", a.UploadSHA1, sumHex(received))
			}
		})
	}
}

func TestUploadFromDiskFailureRecordsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	st, root := diskFixture(t, makeJPEG(t))
	ctx := context.Background()
	if _, err := UploadFromDisk(ctx, sink.NewImmich(immich.New(srv.URL, "k")), st, root, "x"); err == nil {
		t.Fatal("want an error from a 5xx Immich")
	}
	a, _ := st.Get(ctx, "x")
	if a.UploadSHA1 != "" || !a.UploadedAt.IsZero() {
		t.Errorf("failed upload left UploadSHA1 = %q, UploadedAt = %v", a.UploadSHA1, a.UploadedAt)
	}
}
