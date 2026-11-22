package asset

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

func sumHex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}

// The upload checksum is the SHA-1 of the bytes Immich received, and
// the download sha1 keeps meaning "bytes as downloaded", whatever EXIF
// reinjection did in between.
func TestUploadRecordsChecksumOfUploadedBytes(t *testing.T) {
	for _, status := range []string{"created", "duplicate"} {
		t.Run(status, func(t *testing.T) {
			jpegBytes := makeJPEG(t)
			famlySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(jpegBytes)
			}))
			t.Cleanup(famlySrv.Close)

			var received []byte
			immichSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Errorf("parse: %v", err)
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
			t.Cleanup(immichSrv.Close)

			ctx := context.Background()
			disk, err := sink.NewDisk(t.TempDir(), "", "")
			if err != nil {
				t.Fatal(err)
			}
			st, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })

			created := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
			disc := DiscoverImage(
				famly.Image{ImageID: "img-1", BigURL: famlySrv.URL, CreatedAt: famly.ImageTime{Date: famly.FamlyTime{Time: created}}},
				famly.FeedItem{FeedItemID: "post-1", Body: "x", CreatedDate: famly.FamlyTime{Time: created}, Sender: &famly.Sender{Name: "A"}},
				time.UTC)
			if err := st.Discover(ctx, "img-1", state.Asset{Source: string(disc.Source())}); err != nil {
				t.Fatal(err)
			}
			dl, err := disc.Download(ctx, nil, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer dl.Cleanup()
			saved, err := dl.Save(ctx, disk, "bairn test")
			if err != nil {
				t.Fatal(err)
			}
			up, err := saved.Upload(ctx, sink.NewImmich(immich.New(immichSrv.URL, "k")))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := up.Record(ctx, st); err != nil {
				t.Fatal(err)
			}
			if sumHex(received) == sumHex(jpegBytes) {
				t.Fatal("received bytes equal the download bytes; the EXIF fixture should change them, so this test cannot tell upload bytes from download bytes")
			}
			a, _ := st.Get(ctx, "img-1")
			if len(received) == 0 || a.UploadSHA1 != sumHex(received) {
				t.Errorf("UploadSHA1 = %q, want sha1 of received bytes %q", a.UploadSHA1, sumHex(received))
			}
			if a.SHA1 != sumHex(jpegBytes) {
				t.Errorf("SHA1 = %q, want download sha1 %q", a.SHA1, sumHex(jpegBytes))
			}
			if a.ImmichStatus != status {
				t.Errorf("status = %q", a.ImmichStatus)
			}
		})
	}
}
