package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/albums"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// scriptedImmich answers /assets with the next status in script
// (HTTP 400 for "fail", never retried by the client), repeating the
// last entry, and records each request's file parts and fileCreatedAt for inspection.
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
		bodies = append(bodies, strings.Join(parts, ",")+" @"+r.FormValue("fileCreatedAt"))
		if script[i] == "forbid" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
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
	imSrv, hits, bodies := scriptedImmich(t, "fail", "created")
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

	// The retry carries the same timeline date as the first attempt:
	// the post's createdDate, not the retry's clock.
	const wantDate = "@2026-05-06T14:00:00Z"
	if len(*bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(*bodies))
	}
	for i, b := range *bodies {
		if !strings.HasSuffix(b, wantDate) {
			t.Errorf("request %d = %q, want fileCreatedAt %s", i, b, wantDate)
		}
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
"videos":[{"videoId":"vid-1","videoUrl":"%s/v/1"}]}]}`, famlySrv.URL)
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

// manyImagesFamly serves one page of n images, then an empty page.
func manyImagesFamly(t *testing.T, n int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	body := jpegBytes(t)
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			_, _ = w.Write([]byte(`{"feedItems": []}`))
			return
		}
		var imgs []string
		for i := 0; i < n; i++ {
			imgs = append(imgs, fmt.Sprintf(`{"imageId":"img-%d","url":"%s/img/%d","createdAt":{"date":"2026-05-06T14:00:00Z"}}`, i, srv.URL, i))
		}
		fmt.Fprintf(w, `{"feedItems":[{"feedItemId":"p","originatorId":"Post:e",
"createdDate":"2026-05-06T14:00:00Z","body":"b","images":[%s]}]}`, strings.Join(imgs, ","))
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body)
	})
	return srv
}

func TestUploadsStopAfterConsecutiveFailures(t *testing.T) {
	famlySrv := manyImagesFamly(t, 8)
	imSrv, hits, _ := scriptedImmich(t, "fail", "fail", "fail", "fail", "fail", "created")
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	im := sink.NewImmich(immich.New(imSrv.URL, "k"))
	st := openTestStore(t)

	r1 := runWith(t, famlySrv, disk, im, st)
	if r1.Saved != 8 || r1.UploadFailed != 8 || r1.Uploaded != 0 {
		t.Fatalf("run1 saved=%d failed=%d uploaded=%d, want 8/8/0", r1.Saved, r1.UploadFailed, r1.Uploaded)
	}
	if hits.Load() != maxUploadFailures {
		t.Fatalf("immich hits = %d, want %d", hits.Load(), maxUploadFailures)
	}

	// The rest were saved without being sent; a rerun uploads all 8.
	r2 := runWith(t, famlySrv, disk, im, st)
	if r2.Uploaded != 8 || r2.UploadFailed != 0 {
		t.Fatalf("run2 uploaded=%d failed=%d, want 8/0", r2.Uploaded, r2.UploadFailed)
	}
}

func TestUploadsStopAtOnceOnForbidden(t *testing.T) {
	famlySrv := manyImagesFamly(t, 4)
	imSrv, hits, _ := scriptedImmich(t, "forbid")
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	st := openTestStore(t)

	r := runWith(t, famlySrv, disk, sink.NewImmich(immich.New(imSrv.URL, "k")), st)
	if hits.Load() != 1 || r.UploadFailed != 4 || r.Saved != 4 {
		t.Fatalf("hits=%d failed=%d saved=%d, want 1/4/4", hits.Load(), r.UploadFailed, r.Saved)
	}
}

// albumImmich is a stateful fake of the Immich album surface.
type albumImmich struct {
	failAdds atomic.Bool
	refuse   map[string]string // asset ID -> per-item error
	puts     atomic.Int32
	created  []string
	added    map[string][]string
}

func newAlbumImmich(t *testing.T) (*httptest.Server, *albumImmich) {
	t.Helper()
	a := &albumImmich{added: map[string][]string{}}
	names := map[string]string{}
	uploads := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/assets", func(w http.ResponseWriter, r *http.Request) {
		status := "created"
		if uploads++; uploads > 1 {
			status = "duplicate"
		}
		_, _ = fmt.Fprintf(w, `{"id":"asset-1","status":%q}`, status)
	})
	mux.HandleFunc("GET /albums", func(w http.ResponseWriter, r *http.Request) {
		n := r.URL.Query().Get("name")
		if id, ok := names[n]; ok {
			_, _ = fmt.Fprintf(w, `[{"id":%q,"albumName":%q}]`, id, n)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("POST /albums", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ AlbumName string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		a.created = append(a.created, in.AlbumName)
		names[in.AlbumName] = "al-" + in.AlbumName
		_, _ = fmt.Fprintf(w, `{"id":%q}`, names[in.AlbumName])
	})
	mux.HandleFunc("PUT /albums/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		if a.failAdds.Load() {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		var in struct{ IDs []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		a.puts.Add(1)
		var results []string
		for _, id := range in.IDs {
			switch {
			case a.refuse[id] != "":
				results = append(results, fmt.Sprintf(`{"id":%q,"success":false,"error":%q}`, id, a.refuse[id]))
			case slices.Contains(a.added[r.PathValue("id")], id):
				a.added[r.PathValue("id")] = append(a.added[r.PathValue("id")], id)
				results = append(results, fmt.Sprintf(`{"id":%q,"success":false,"error":"duplicate"}`, id))
			default:
				a.added[r.PathValue("id")] = append(a.added[r.PathValue("id")], id)
				results = append(results, fmt.Sprintf(`{"id":%q,"success":true}`, id))
			}
		}
		_, _ = w.Write([]byte("[" + strings.Join(results, ",") + "]"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, a
}

func TestUploadsAreFiledInMonthAndYearAlbumsIdempotently(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, fake := newAlbumImmich(t)
	ic := immich.New(srv.URL, "k")
	for run := range 2 {
		disk, _ := sink.NewDisk(t.TempDir(), "", "")
		fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(famlySrv.URL), famly.WithPageDelay(0))
		filer := albums.New(ic, albums.Config{MonthPattern: "EDC %B %Y", YearPattern: "EDC %Y", Zone: time.UTC}, nil)
		// A fresh state each run models a lost state file: the asset
		// uploads again, Immich answers duplicate, albums stay put.
		res, err := Run(context.Background(), Deps{Famly: fc, Disk: disk, Immich: sink.NewImmich(ic), Albums: filer, State: openTestStore(t)},
			Options{MaxPages: 2, Source: SourceAll})
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if res.AlbumErrors != 0 {
			t.Errorf("run %d: AlbumErrors = %d, want 0", run, res.AlbumErrors)
		}
	}
	slices.Sort(fake.created)
	if !slices.Equal(fake.created, []string{"EDC 2026", "EDC May 2026"}) {
		t.Errorf("created = %v, want each album exactly once", fake.created)
	}
	if len(fake.added["al-EDC May 2026"]) != 2 {
		t.Errorf("month adds = %v, want one per run", fake.added)
	}
}

func albumRun(t *testing.T, famlySrv *httptest.Server, ic *immich.Client, st *state.Store) Result {
	t.Helper()
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(famlySrv.URL), famly.WithPageDelay(0))
	filer := albums.New(ic, albums.Config{MonthPattern: "EDC %B %Y", YearPattern: "EDC %Y", Zone: time.UTC}, nil)
	res, err := Run(context.Background(), Deps{Famly: fc, Disk: disk, Immich: sink.NewImmich(ic), Albums: filer, State: st},
		Options{MaxPages: 2, Source: SourceAll})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func unfiledCount(st *state.Store) int { return len(st.Unfiled()) }

func TestFailedFinalFlushIsRefiledOnRerun(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, fake := newAlbumImmich(t)
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)

	fake.failAdds.Store(true)
	if res := albumRun(t, famlySrv, ic, st); res.AlbumErrors == 0 {
		t.Fatal("first run: want album errors")
	}
	if unfiledCount(st) == 0 {
		t.Fatal("failed flush left no unfiled asset in state")
	}

	fake.failAdds.Store(false)
	res := albumRun(t, famlySrv, ic, st)
	if res.AlbumErrors != 0 {
		t.Fatalf("rerun AlbumErrors = %d", res.AlbumErrors)
	}
	if len(fake.added["al-EDC 2026"]) == 0 || len(fake.added["al-EDC May 2026"]) == 0 {
		t.Errorf("rerun did not file: %v", fake.added)
	}
	if unfiledCount(st) != 0 {
		t.Errorf("unfiled after rerun = %d, want 0", unfiledCount(st))
	}
}

func TestCrashBeforeFilingIsRefiledFromStateFile(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, fake := newAlbumImmich(t)
	ic := immich.New(srv.URL, "k")
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	fake.failAdds.Store(true)
	albumRun(t, famlySrv, ic, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	fake.failAdds.Store(false)
	st2, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st2.Close() })
	if unfiledCount(st2) == 0 {
		t.Fatal("reopened state has no unfiled asset")
	}
	if res := albumRun(t, famlySrv, ic, st2); res.AlbumErrors != 0 {
		t.Fatalf("AlbumErrors = %d", res.AlbumErrors)
	}
	if len(fake.added["al-EDC May 2026"]) == 0 || unfiledCount(st2) != 0 {
		t.Errorf("not filed after crash rerun: %v unfiled=%d", fake.added, unfiledCount(st2))
	}
}

func TestEntriesWithoutPostDateAreNeverRequeued(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, fake := newAlbumImmich(t)
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.Discover(ctx, "old-1", state.Asset{Source: "feed-image", DiscoveredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkUploaded(ctx, "old-1", "old-immich", "created", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	albumRun(t, famlySrv, ic, st)
	for id, got := range fake.added {
		if slices.Contains(got, "old-immich") {
			t.Errorf("old entry was filed into %s", id)
		}
	}
	a, _ := st.Get(ctx, "old-1")
	if !a.PostedAt.IsZero() || !a.AlbumsFiledAt.IsZero() {
		t.Errorf("old entry modified: %+v", a)
	}
}

func TestLegacyEntryIsFiledFromDownloadedAt(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, fake := newAlbumImmich(t)
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)
	ctx := context.Background()
	posted := time.Date(2025, 12, 31, 23, 30, 0, 0, time.UTC)
	if err := st.Discover(ctx, "legacy-1", state.Asset{Source: "feed-image", DiscoveredAt: time.Now(), DownloadedAt: posted}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkUploaded(ctx, "legacy-1", "legacy-immich", "created", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if res := albumRun(t, famlySrv, ic, st); res.AlbumErrors != 0 {
		t.Fatalf("AlbumErrors = %d", res.AlbumErrors)
	}
	if !slices.Contains(fake.added["al-EDC December 2025"], "legacy-immich") || !slices.Contains(fake.added["al-EDC 2025"], "legacy-immich") {
		t.Errorf("legacy asset not filed under its post date: %v", fake.added)
	}
	a, _ := st.Get(ctx, "legacy-1")
	if !a.PostedAt.Equal(posted) || a.AlbumsFiledAt.IsZero() {
		t.Errorf("legacy entry not backfilled: %+v", a)
	}
	puts := fake.puts.Load()
	albumRun(t, famlySrv, ic, st)
	if fake.puts.Load() != puts {
		t.Error("second run re-sent the legacy asset")
	}
}

func TestRefusedAssetIsRecordedAndNotRetried(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, fake := newAlbumImmich(t)
	fake.refuse = map[string]string{"gone-immich": "no_permission"}
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)
	ctx := context.Background()
	posted := time.Date(2025, 12, 31, 23, 30, 0, 0, time.UTC)
	for _, id := range []string{"gone-1", "kept-1"} {
		if err := st.Discover(ctx, id, state.Asset{Source: "feed-image", DiscoveredAt: time.Now(), DownloadedAt: posted}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.MarkUploaded(ctx, "gone-1", "gone-immich", "created", "", time.Now())
	_ = st.MarkUploaded(ctx, "kept-1", "kept-immich", "created", "", time.Now())

	res := albumRun(t, famlySrv, ic, st)
	if res.AlbumErrors != 0 || res.AlbumsRefused != 1 {
		t.Fatalf("AlbumErrors=%d AlbumsRefused=%d, want 0 and 1", res.AlbumErrors, res.AlbumsRefused)
	}
	gone, _ := st.Get(ctx, "gone-1")
	kept, _ := st.Get(ctx, "kept-1")
	if gone.AlbumsRefused != "no_permission" || !gone.AlbumsFiledAt.IsZero() {
		t.Errorf("refused entry = %+v", gone)
	}
	if kept.AlbumsFiledAt.IsZero() {
		t.Errorf("accepted entry not filed: %+v", kept)
	}
	puts := fake.puts.Load()
	if res := albumRun(t, famlySrv, ic, st); res.AlbumErrors != 0 || res.AlbumsRefused != 0 {
		t.Errorf("rerun: %+v", res)
	}
	if fake.puts.Load() != puts {
		t.Error("rerun sent the refused or filed assets again")
	}
}

func TestDryRunLeavesStateEmpty(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	st := openTestStore(t)
	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(famlySrv.URL), famly.WithPageDelay(0))
	if _, err := Run(context.Background(), Deps{Famly: fc, Disk: disk, State: st},
		Options{MaxPages: 2, Source: SourceAll, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if sum, _ := st.Stats(context.Background()); sum.Total != 0 {
		t.Errorf("dry run recorded %d state entries", sum.Total)
	}
}

// A state file that stops accepting writes after the uploads (ENOSPC
// in production) must be counted, so the CLI cannot exit 0.
func TestStateRecordFailureIsCounted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	inner, _ := newAlbumImmich(t)
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetFlushEvery(1)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700); _ = st.Close() })
	// Make the state dir read-only once the album adds begin: every
	// state write after that fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/assets") && r.Method == http.MethodPut {
			_ = os.Chmod(dir, 0o500)
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	res := albumRun(t, famlySrv, immich.New(srv.URL, "k"), st)
	if res.StateErrors == 0 {
		t.Fatalf("StateErrors = 0, want > 0 (result %+v)", res)
	}
}
