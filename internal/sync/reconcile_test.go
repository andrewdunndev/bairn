package sync

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
)

// mixedFeed serves one page holding every kind of item a run can meet:
// good, dup (Immich answers duplicate) and bad (CDN 404) images, one
// untagged-vs-tagged pair for the source filter, a system post with two
// images, a transcoding video and a ready video.
func mixedFeed(t *testing.T) *httptest.Server {
	t.Helper()
	body := jpegBytes(t)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	img := func(id string, tagged bool) string {
		tag := "[]"
		if tagged {
			tag = `[{"tag":"c","type":"Child","name":"Child A","childId":"child-1"}]`
		}
		return fmt.Sprintf(`{"imageId":%q,"url":"%[2]s/img/%[1]s","url_big":"%[2]s/img/%[1]s","createdAt":{"date":"2026-05-06T14:00:00Z"},"tags":%[3]s}`, id, srv.URL, tag)
	}
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			_, _ = w.Write([]byte(`{"feedItems":[]}`))
			return
		}
		fmt.Fprintf(w, `{"feedItems":[
{"feedItemId":"p1","createdDate":"2026-05-06T14:00:00Z","images":[%s,%s,%s,%s],
 "videos":[{"videoId":"vid-ready","videoUrl":"%s/img/vid-ready","transcoding":false},{"videoId":"vid-wait","videoUrl":"","transcoding":true}]},
{"feedItemId":"p2","createdDate":"2026-05-05T14:00:00Z","systemPostTypeClass":"CheckIn","images":[%s,%s],"videos":[]}
]}`, img("good", true), img("dup", false), img("bad", false), img("plain", false), srv.URL, img("sys1", false), img("sys2", false))
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/bad") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	})
	return srv
}

func dupImmich(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		status := "created"
		for _, fh := range r.MultipartForm.File["assetData"] {
			if strings.Contains(fh.Filename, "dup") {
				status = "duplicate"
			}
		}
		fmt.Fprintf(w, `{"id":"a-%d","status":%q}`, len(r.URL.Path)+int(r.ContentLength%7), status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEveryDiscoveredItemLandsInExactlyOneBucket(t *testing.T) {
	feed := mixedFeed(t)
	im := dupImmich(t)
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	st := openTestStore(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(feed.URL), famly.WithPageDelay(0))
	run := func(src Source, dry bool) Result {
		deps := Deps{Famly: fc, Disk: disk, State: st, Logger: logger, DownloadRetry: instantRetry(1),
			Immich: sink.NewImmich(immich.New(im.URL, "k"))}
		res, err := Run(context.Background(), deps, Options{Source: src, DryRun: dry,
			HouseholdChildren: map[string]struct{}{"child-1": {}}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	check := func(name string, res Result, want Buckets, discovered int) {
		t.Helper()
		if res.Buckets != want {
			t.Errorf("%s: buckets = %+v, want %+v", name, res.Buckets, want)
		}
		if res.Discovered != discovered || res.Buckets.Total() != res.Discovered || res.Unreconciled != 0 {
			t.Errorf("%s: discovered=%d bucketTotal=%d unreconciled=%d, want %d summing", name, res.Discovered, res.Buckets.Total(), res.Unreconciled, discovered)
		}
	}

	// Run 1: good, plain and the ready video upload as new; dup as an
	// Immich duplicate; bad fails; the transcoding video is not ready;
	// the two system-post images are skipped. 10 items.
	r1 := run(SourceAll, false)
	check("run1", r1, Buckets{UploadedNew: 3, UploadedDuplicate: 1, Failed: 1, NotReady: 1, SkippedSystem: 2}, 8)
	// The legacy counters count different things, which is why they never
	// summed: Saved is files written, Uploaded only Immich-created.
	if r1.Saved != 4 || r1.Uploaded != 3 || r1.UploadDuplicates != 1 || r1.UploadFailed != 0 {
		t.Errorf("run1 legacy counters = saved %d uploaded %d dup %d", r1.Saved, r1.Uploaded, r1.UploadDuplicates)
	}

	// Run 2: all of that is done; only bad is attempted (and fails again).
	r2 := run(SourceAll, false)
	check("run2", r2, Buckets{AlreadyDone: 4, Failed: 1, NotReady: 1, SkippedSystem: 2}, 8)

	// Source filter: tagged keeps only good; every other image and the
	// videos are filtered.
	r3 := run(SourceTagged, false)
	check("run3", r3, Buckets{AlreadyDone: 1, FilteredBySource: 5, SkippedSystem: 2}, 8)

	// Dry run: bad is the one item a real run would act on.
	r4 := run(SourceAll, true)
	check("run4", r4, Buckets{AlreadyDone: 4, WouldProcess: 1, NotReady: 1, SkippedSystem: 2}, 8)
}

func TestReconcileLogsAnErrorWhenTheBucketsDoNotAddUp(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	res := Result{Discovered: 5}
	res.Buckets.add(bSavedNew)
	res.Buckets.add(bFailed)
	if res.Reconcile(logger) {
		t.Fatal("Reconcile accepted 2 bucketed of 5 discovered")
	}
	if res.Unreconciled != 3 {
		t.Errorf("Unreconciled = %d, want 3", res.Unreconciled)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "does not reconcile") {
		t.Errorf("no error logged: %s", logs.String())
	}
	ok := Result{Discovered: 1}
	ok.Buckets.add(bAlreadyDone)
	if !ok.Reconcile(logger) {
		t.Error("a matching result was refused")
	}
}
