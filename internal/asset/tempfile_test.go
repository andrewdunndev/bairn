package asset

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
)

func tempTestDisc(url string) Discovered {
	created := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	img := famly.Image{ImageID: "img-t", BigURL: url, CreatedAt: famly.ImageTime{Date: famly.FamlyTime{Time: created}}}
	item := famly.FeedItem{FeedItemID: "post-t", CreatedDate: famly.FamlyTime{Time: created}}
	return DiscoverImage(img, item, time.UTC)
}

func TestDownloadTempLivesInDestDirDotPrefixed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("bytes"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	dl, err := tempTestDisc(srv.URL).Download(context.Background(), nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dl.TempPath()) != dir || !strings.HasPrefix(filepath.Base(dl.TempPath()), TempPrefix) {
		t.Fatalf("temp %q not a dot-prefixed file in %q", dl.TempPath(), dir)
	}
	dl.Cleanup()
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("Cleanup left %d files", len(left))
	}
}

func TestDownloadFailureLeavesNoTemp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	if _, err := tempTestDisc(srv.URL).Download(context.Background(), nil, dir); err == nil {
		t.Fatal("expected a stream error")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("failed download left %d files", len(left))
	}
}
