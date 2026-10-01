package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// scriptedImmich answers /assets with the next status in script
// (HTTP 400 for "fail", never retried by the client), repeating the
// last entry, and records each request's body for inspection.
func scriptedImmich(t *testing.T, script ...string) (*httptest.Server, *atomic.Int32, *[]string) {
	t.Helper()
	var n atomic.Int32
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i >= len(script) {
			i = len(script) - 1
		}
		_ = r.ParseMultipartForm(1 << 20)
		var parts []string
		for k := range r.MultipartForm.File {
			parts = append(parts, k)
		}
		bodies = append(bodies, strings.Join(parts, ","))
		if script[i] == "fail" {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"asset-%d","status":%q}`, i, script[i])
	}))
	t.Cleanup(srv.Close)
	return srv, &n, &bodies
}

func runWith(t *testing.T, famlySrv *httptest.Server, disk *sink.Disk, im *sink.Immich, st *state.Store) Result {
	t.Helper()
	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(famlySrv.URL), famly.WithPageDelay(0))
	res, err := Run(context.Background(), Deps{Famly: fc, Disk: disk, Immich: im, State: st},
		Options{MaxPages: 2, Source: SourceAll})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestFailedUploadIsRetriedFromDiskOnRerun(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	imSrv, hits, _ := scriptedImmich(t, "fail", "created")
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	im := sink.NewImmich(immich.New(imSrv.URL, "k"))
	st := openTestStore(t)

	r1 := runWith(t, famlySrv, disk, im, st)
	if r1.Saved != 1 || r1.UploadFailed != 1 || r1.Uploaded != 0 {
		t.Fatalf("run1 saved=%d failed=%d uploaded=%d, want 1/1/0", r1.Saved, r1.UploadFailed, r1.Uploaded)
	}
	if up, _ := st.IsUploaded(context.Background(), "img-1"); up {
		t.Fatal("failed upload must not be recorded as uploaded")
	}

	r2 := runWith(t, famlySrv, disk, im, st)
	if r2.Uploaded != 1 || r2.UploadFailed != 0 || r2.Saved != 0 {
		t.Fatalf("run2 uploaded=%d failed=%d saved=%d, want 1/0/0", r2.Uploaded, r2.UploadFailed, r2.Saved)
	}
	if up, _ := st.IsUploaded(context.Background(), "img-1"); !up {
		t.Fatal("retry should record the upload")
	}

	r3 := runWith(t, famlySrv, disk, im, st)
	if r3.Uploaded != 0 || r3.Skipped != 1 || hits.Load() != 2 {
		t.Fatalf("run3 uploaded=%d skipped=%d hits=%d, want 0/1/2", r3.Uploaded, r3.Skipped, hits.Load())
	}
}

func TestDuplicateUploadResponseCountsAsConfirmed(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	imSrv, _, _ := scriptedImmich(t, "duplicate")
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	st := openTestStore(t)

	r := runWith(t, famlySrv, disk, sink.NewImmich(immich.New(imSrv.URL, "k")), st)
	if r.UploadDuplicates != 1 || r.Uploaded != 0 || r.UploadFailed != 0 {
		t.Fatalf("dup=%d uploaded=%d failed=%d, want 1/0/0", r.UploadDuplicates, r.Uploaded, r.UploadFailed)
	}
	if up, _ := st.IsUploaded(context.Background(), "img-1"); !up {
		t.Fatal("duplicate response must count as confirmed")
	}
}

// A save-only run (or a state entry from before Immich was wired in)
// leaves assets saved without an upload; a later run with Immich
// uploads them from disk.
func TestSavedOnlyAssetsUploadWhenImmichAppears(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	imSrv, hits, _ := scriptedImmich(t, "created")
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	st := openTestStore(t)

	runWith(t, famlySrv, disk, nil, st)
	r := runWith(t, famlySrv, disk, sink.NewImmich(immich.New(imSrv.URL, "k")), st)
	if r.Uploaded != 1 || hits.Load() != 1 {
		t.Fatalf("uploaded=%d hits=%d, want 1/1", r.Uploaded, hits.Load())
	}
}

func TestDryRunDoesNotUploadSavedAssets(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	imSrv, hits, _ := scriptedImmich(t, "created")
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	st := openTestStore(t)
	runWith(t, famlySrv, disk, nil, st)

	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(famlySrv.URL), famly.WithPageDelay(0))
	_, err := Run(context.Background(), Deps{Famly: fc, Disk: disk, State: st,
		Immich: sink.NewImmich(immich.New(imSrv.URL, "k"))},
		Options{MaxPages: 2, Source: SourceAll, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatalf("dry run uploaded %d times", hits.Load())
	}
}

func TestVideoRetryResendsSidecarFromDisk(t *testing.T) {
	mux := http.NewServeMux()
	famlySrv := httptest.NewServer(mux)
	t.Cleanup(famlySrv.Close)
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			_, _ = w.Write([]byte(`{"feedItems": []}`))
			return
		}
		fmt.Fprintf(w, `{"feedItems":[{"feedItemId":"p","originatorId":"Post:e",
"createdDate":"2026-05-06T14:00:00Z","body":"clip","images":[],
"videos":[{"videoId":"vid-1","url":"%s/v/1"}]}]}`, famlySrv.URL)
	})
	mux.HandleFunc("/v/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("not really a video"))
	})
	imSrv, _, parts := scriptedImmich(t, "fail", "created")
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	im := sink.NewImmich(immich.New(imSrv.URL, "k"))
	st := openTestStore(t)

	runWith(t, famlySrv, disk, im, st)
	r := runWith(t, famlySrv, disk, im, st)
	if r.Uploaded != 1 {
		t.Fatalf("uploaded=%d, want 1", r.Uploaded)
	}
	if len(*parts) != 2 || !strings.Contains((*parts)[1], "sidecarData") {
		t.Fatalf("retry upload parts = %v, want sidecarData present", *parts)
	}
}
