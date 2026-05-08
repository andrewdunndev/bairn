package sync

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// jpegBytes returns a tiny solid-colour JPEG. Used as the body
// served by the Famly fixture for image downloads.
func jpegBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{0xff, 0x80, 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// fakeFamlyServer serves two pages of feed (one image on first
// page, empty second page) plus the JPEG bytes for that image.
func fakeFamlyServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	body := jpegBytes(t)
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		isFirstPage := r.URL.Query().Get("cursor") == ""
		w.Header().Set("Content-Type", "application/json")
		if isFirstPage {
			fmt.Fprintf(w, `{
  "feedItems": [
    {
      "feedItemId": "post-1",
      "originatorId": "Post:e1",
      "createdDate": "2026-05-06T14:00:00Z",
      "body": "post 1",
      "sender": {"id": "u1", "loginId": "u1", "name": "Educator A"},
      "images": [
        {"imageId": "img-1", "url": "%[1]s/img/1", "url_big": "%[1]s/img/1/big",
         "width": 800, "height": 600,
         "createdAt": {"date": "2026-05-06T14:00:00Z"},
         "expiration": "2026-05-07T14:00:00Z",
         "liked": false, "likes": [], "tags": []}
      ],
      "videos": []
    }
  ]
}`, srv.URL)
		} else {
			_, _ = w.Write([]byte(`{"feedItems": []}`))
		}
	})
	mux.HandleFunc("/img/1/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
	return srv
}

func fakeImmichServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/assets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"asset-1","status":"created"}`))
	})
	return httptest.NewServer(mux)
}

func openTestStore(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Open state: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRunSavesAndUploadsSourceAll(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	immichSrv := fakeImmichServer(t)
	t.Cleanup(immichSrv.Close)

	saveDir := t.TempDir()
	disk, err := sink.NewDisk(saveDir, "", "")
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	immichSink := sink.NewImmich(immich.New(immichSrv.URL, "test-key"))
	st := openTestStore(t)

	fc := famly.New(famly.NewStaticToken("test-token"), famly.WithBaseURL(famlySrv.URL))

	res, err := Run(context.Background(), Deps{
		Famly: fc, Disk: disk, Immich: immichSink, State: st,
	}, Options{
		MaxPages: 2,
		Source:   SourceAll,
		Software: "bairn 0.1-test",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Discovered != 1 {
		t.Errorf("Discovered = %d, want 1", res.Discovered)
	}
	if res.Saved != 1 {
		t.Errorf("Saved = %d, want 1", res.Saved)
	}
	if res.Uploaded != 1 {
		t.Errorf("Uploaded = %d, want 1", res.Uploaded)
	}
	if res.Errors != 0 {
		t.Errorf("Errors = %d, want 0", res.Errors)
	}

	// File should exist on disk.
	matches, _ := filepath.Glob(filepath.Join(saveDir, "*", "feed-image-*.jpg"))
	if len(matches) != 1 {
		t.Errorf("expected 1 saved file, got %d: %v", len(matches), matches)
	}

	// Idempotency: a second run skips it.
	res2, err := Run(context.Background(), Deps{
		Famly: fc, Disk: disk, Immich: immichSink, State: st,
	}, Options{
		MaxPages: 2,
		Source:   SourceAll,
		Software: "bairn 0.1-test",
	})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res2.Saved != 0 || res2.Uploaded != 0 {
		t.Errorf("second run saved=%d uploaded=%d, want 0/0", res2.Saved, res2.Uploaded)
	}
	if res2.Skipped == 0 {
		t.Error("second run should skip")
	}
}

func TestRunSavesOnlyWithoutImmich(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)

	saveDir := t.TempDir()
	disk, _ := sink.NewDisk(saveDir, "", "")
	st := openTestStore(t)

	fc := famly.New(famly.NewStaticToken("test-token"), famly.WithBaseURL(famlySrv.URL))

	res, err := Run(context.Background(), Deps{
		Famly: fc, Disk: disk, Immich: nil, State: st,
	}, Options{
		MaxPages: 2,
		Source:   SourceAll,
		Software: "bairn 0.1-test",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Saved != 1 {
		t.Errorf("Saved = %d, want 1", res.Saved)
	}
	if res.Uploaded != 0 {
		t.Errorf("Uploaded = %d, want 0 (no Immich configured)", res.Uploaded)
	}
}

func TestRunSourceTaggedFiltersByChild(t *testing.T) {
	mux := http.NewServeMux()
	famlySrv := httptest.NewServer(mux)
	t.Cleanup(famlySrv.Close)
	body := jpegBytes(t)
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
  "feedItems": [
    {
      "feedItemId": "p", "originatorId": "Post:e",
      "createdDate": "2026-05-06T14:00:00Z", "body": "",
      "images": [
        {"imageId": "img-tagged", "url": "%[1]s/i/t", "url_big": "%[1]s/i/t/b",
         "createdAt": {"date": "2026-05-06T14:00:00Z"},
         "expiration": "2026-05-07T14:00:00Z",
         "liked": false, "likes": [],
         "tags": [{"tag":"c","type":"Child","name":"C","childId":"our-child"}]},
        {"imageId": "img-untagged", "url": "%[1]s/i/u", "url_big": "%[1]s/i/u/b",
         "createdAt": {"date": "2026-05-06T14:00:00Z"},
         "expiration": "2026-05-07T14:00:00Z",
         "liked": false, "likes": [], "tags": []}
      ],
      "videos": []
    }
  ]
}`, famlySrv.URL)
	})
	mux.HandleFunc("/i/t/b", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })

	saveDir := t.TempDir()
	disk, _ := sink.NewDisk(saveDir, "", "")
	st := openTestStore(t)

	fc := famly.New(famly.NewStaticToken("test-token"), famly.WithBaseURL(famlySrv.URL))

	res, err := Run(context.Background(), Deps{Famly: fc, Disk: disk, State: st}, Options{
		MaxPages:          1,
		Source:            SourceTagged,
		HouseholdChildren: map[string]struct{}{"our-child": {}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Saved != 1 {
		t.Errorf("Saved = %d, want 1 (only tagged image)", res.Saved)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (untagged image)", res.Skipped)
	}
}

// Sentinel test: the temp file produced by Download is removed by
// Cleanup() and the Save rename. After a successful Run, no
// bairn-dl-* tempfiles linger in $TMPDIR.
func TestRunCleansTempFiles(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	st := openTestStore(t)
	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(famlySrv.URL))

	if _, err := Run(context.Background(), Deps{Famly: fc, Disk: disk, State: st}, Options{
		MaxPages: 1,
		Source:   SourceAll,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// $TMPDIR should not have bairn-dl-* leftovers from this run.
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "bairn-dl-*"))
	if len(matches) > 0 {
		t.Errorf("temp files leaked: %v", matches)
	}
}
