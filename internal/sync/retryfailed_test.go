package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

var feedBase = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

func postTime(n int) time.Time { return feedBase.Add(time.Duration(n) * time.Hour) }

// datedFeed serves n posts, one image each, newest (n-1) first, 10 per
// page, honouring olderThan and cursor like Famly. Image i is served
// from /img/i unless i is in gone, which answers 404. It records the
// query of every feed request.
type datedFeed struct {
	*httptest.Server
	mu      sync.Mutex
	queries []string
	pages   int
	// failFrom, when >0, answers 400 from that feed request on.
	failFrom int
}

func newDatedFeed(t *testing.T, n int, gone map[int]bool, ignoreOlderThan bool) *datedFeed {
	t.Helper()
	body := jpegBytes(t)
	f := &datedFeed{}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.RawQuery)
		f.pages++
		n0 := f.pages
		f.mu.Unlock()
		if f.failFrom > 0 && n0 >= f.failFrom {
			http.Error(w, "boom", http.StatusBadRequest)
			return
		}
		hi := n - 1
		if c := q.Get("cursor"); c != "" {
			id, _ := strconv.Atoi(c[len("post-"):])
			hi = id - 1
		} else if ot := q.Get("olderThan"); ot != "" && !ignoreOlderThan {
			at, err := time.Parse(time.RFC3339, ot)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			for hi >= 0 && !postTime(hi).Before(at) {
				hi--
			}
		}
		out := `{"feedItems":[`
		for i, c := hi, 0; i >= 0 && c < FeedPageSizeForTest; i, c = i-1, c+1 {
			if c > 0 {
				out += ","
			}
			out += fmt.Sprintf(`{"feedItemId":"post-%[1]d","createdDate":%[2]q,"images":[{"imageId":"img-%[1]d","url":"%[3]s/img/%[1]d","url_big":"%[3]s/img/%[1]d","createdAt":{"date":%[2]q}}],"videos":[]}`,
				i, postTime(i).Format(time.RFC3339), f.URL)
		}
		_, _ = w.Write([]byte(out + `]}`))
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		i, _ := strconv.Atoi(r.URL.Path[len("/img/"):])
		if gone[i] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	})
	return f
}

// FeedPageSizeForTest mirrors famly.FeedPageSize.
const FeedPageSizeForTest = famly.FeedPageSize

type retryEnv struct {
	feed *datedFeed
	deps Deps
	st   *state.Store
	dir  string
	path string
}

// seedFailed records the images as discovered and failed, with the post
// date when dated.
func seedFailed(t *testing.T, st *state.Store, dated bool, ids ...int) {
	t.Helper()
	for _, i := range ids {
		a := state.Asset{Source: "feed-image", FeedItemID: fmt.Sprintf("post-%d", i), DiscoveredAt: postTime(i).Add(time.Minute)}
		if dated {
			a.FeedItemAt = postTime(i)
		}
		id := fmt.Sprintf("img-%d", i)
		if err := st.Discover(context.Background(), id, a); err != nil {
			t.Fatal(err)
		}
		_ = st.MarkError(context.Background(), id, "asset: stream body: connection reset by peer")
	}
}

func newRetryEnv(t *testing.T, feed *datedFeed) *retryEnv {
	t.Helper()
	dir := t.TempDir()
	disk, _ := sink.NewDisk(dir, "", "")
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(feed.URL), famly.WithPageDelay(0))
	return &retryEnv{feed: feed, st: st, dir: dir, path: path,
		deps: Deps{Famly: fc, Disk: disk, State: st, DownloadRetry: instantRetry(1)}}
}

func (e *retryEnv) run(t *testing.T, dry bool) Result {
	t.Helper()
	res, err := Run(context.Background(), e.deps, Options{Source: SourceAll, RetryFailed: true, DryRun: dry})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRetryFailedJumpsToTheFailedItemsAndStopsAfterThem(t *testing.T) {
	feed := newDatedFeed(t, 100, nil, false)
	e := newRetryEnv(t, feed)
	seedFailed(t, e.st, true, 61, 55)
	res := e.run(t, false)

	if r := res.Retry; r == nil || r.Failed != 2 || r.Retried != 2 || r.Fixed != 2 || r.StillFailing != 0 {
		t.Fatalf("retry summary = %+v, want 2 failed, retried, fixed", r)
	}
	if res.Saved != 2 || res.Discovered != 2 {
		t.Errorf("saved=%d discovered=%d, want 2/2: only the failed items are touched", res.Saved, res.Discovered)
	}
	// 100 posts is 10 pages; the walk jumped past the 38 newer posts and
	// read only the pages down to post 55 (61..52), not the whole feed.
	if feed.pages != 1 {
		t.Errorf("feed pages read = %d (%v), want 1", feed.pages, feed.queries)
	}
	if q := feed.queries[0]; q == "" || !contains(q, "olderThan=") || contains(q, "cursor=") {
		t.Errorf("first request %q did not jump with olderThan alone", q)
	}
	if ok, _ := e.st.IsSaved(context.Background(), "img-61"); !ok {
		t.Error("img-61 not saved")
	}
	if len(e.st.Failed()) != 0 {
		t.Error("fixed entries still listed as failed")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRetryFailedSpansPagesBetweenTheTwoDates(t *testing.T) {
	feed := newDatedFeed(t, 100, nil, false)
	e := newRetryEnv(t, feed)
	seedFailed(t, e.st, true, 80, 40)
	res := e.run(t, false)
	if res.Retry.Fixed != 2 {
		t.Fatalf("summary = %+v", res.Retry)
	}
	// 80..71, 70..61, 60..51, 50..41, 40..31: five pages, not ten.
	if feed.pages != 5 {
		t.Errorf("feed pages = %d, want 5", feed.pages)
	}
}

func TestRetryFailedReportsStillFailingAndNotFound(t *testing.T) {
	feed := newDatedFeed(t, 50, map[int]bool{30: true}, false)
	e := newRetryEnv(t, feed)
	seedFailed(t, e.st, true, 30, 20)
	// An entry whose post is no longer in the feed.
	_ = e.st.Discover(context.Background(), "img-gone", state.Asset{Source: "feed-image", FeedItemID: "post-x", FeedItemAt: postTime(25), DiscoveredAt: postTime(26)})
	_ = e.st.MarkError(context.Background(), "img-gone", "boom")
	res := e.run(t, false)
	r := res.Retry
	if r.Failed != 3 || r.Retried != 2 || r.Fixed != 1 || r.StillFailing != 2 || r.NotFound != 1 {
		t.Errorf("summary = %+v, want failed 3, retried 2, fixed 1, stillFailing 2, notFound 1", r)
	}
	if res.Errors != 1 {
		t.Errorf("Errors = %d, want the one failed retry", res.Errors)
	}
	if got, _ := e.st.Get(context.Background(), "img-30"); got.Retries != 2 || got.LastError == "" {
		t.Errorf("failed retry not recorded: %+v", got)
	}
}

func TestRetryFailedUndatedEntriesFallBackToDiscoveryTime(t *testing.T) {
	feed := newDatedFeed(t, 100, nil, false)
	e := newRetryEnv(t, feed)
	seedFailed(t, e.st, false, 70)
	res := e.run(t, false)
	if r := res.Retry; r.Undated != 1 || r.Fixed != 1 {
		t.Fatalf("summary = %+v, want the undated entry found and fixed", r)
	}
	// Started at its discovery time and stopped as soon as it was found.
	if feed.pages != 1 {
		t.Errorf("feed pages = %d, want 1", feed.pages)
	}
	// The post date is recorded for next time.
	st, _ := e.st.Get(context.Background(), "img-70")
	if !st.FeedItemAt.Equal(postTime(70)) {
		t.Errorf("FeedItemAt = %v, want %v backfilled", st.FeedItemAt, postTime(70))
	}
}

func TestRetryFailedToleratesAServerThatIgnoresOlderThan(t *testing.T) {
	feed := newDatedFeed(t, 40, nil, true)
	e := newRetryEnv(t, feed)
	seedFailed(t, e.st, true, 15)
	res := e.run(t, false)
	if res.Retry.Fixed != 1 || res.Errors != 0 {
		t.Errorf("want a full walk from the head to still find it: %+v errors=%d", res.Retry, res.Errors)
	}
}

func TestRetryFailedDryRunChangesNothing(t *testing.T) {
	feed := newDatedFeed(t, 50, nil, false)
	e := newRetryEnv(t, feed)
	seedFailed(t, e.st, true, 30)
	res := e.run(t, true)
	if r := res.Retry; r.Retried != 1 || r.Fixed != 0 || r.StillFailing != 0 || r.NotFound != 0 {
		t.Errorf("summary = %+v, want 1 would-retry and no fixed/stillFailing", r)
	}
	if ok, _ := e.st.IsSaved(context.Background(), "img-30"); ok {
		t.Error("dry run saved an asset")
	}
	if matches, _ := filepath.Glob(filepath.Join(e.dir, "*", "*")); len(matches) != 0 {
		t.Errorf("dry run wrote files: %v", matches)
	}
	if got, _ := e.st.Get(context.Background(), "img-30"); got.Retries != 1 {
		t.Errorf("dry run touched the entry: %+v", got)
	}
}

func TestRetryFailedWithNothingFailedReadsNoFeed(t *testing.T) {
	feed := newDatedFeed(t, 10, nil, false)
	e := newRetryEnv(t, feed)
	res := e.run(t, false)
	if res.Retry == nil || res.Retry.Failed != 0 || feed.pages != 0 {
		t.Errorf("summary = %+v pages=%d, want none", res.Retry, feed.pages)
	}
}

func TestRetryPlanWithNothingFailedLogsNoneForTimes(t *testing.T) {
	p := newRetryPlan(nil)
	if got := timeOrNone(p.start); got != "none" {
		t.Errorf("jumpTo = %q, want none", got)
	}
	if got := timeOrNone(p.floor); got != "none" {
		t.Errorf("stopAfter = %q, want none", got)
	}
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if got := timeOrNone(when); got != "2026-03-04T05:06:07Z" {
		t.Errorf("timeOrNone(set) = %q", got)
	}
}

func TestSavedAssetIsNoLongerFailed(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_ = st.Discover(ctx, "a", state.Asset{Source: "feed-image"})
	_ = st.MarkError(ctx, "a", "x")
	if len(st.Failed()) != 1 {
		t.Fatal("want one failed")
	}
	_ = st.MarkSaved(ctx, "a", "/p", time.Now())
	if len(st.Failed()) != 0 {
		t.Error("saved entry still failed")
	}
}

func TestRetryFailedStopsAtTheFloorWhenADatedTargetIsMissing(t *testing.T) {
	feed := newDatedFeed(t, 100, nil, false)
	e := newRetryEnv(t, feed)
	seedFailed(t, e.st, true, 40)
	// Dated, but no such post in the feed: the walk must not run on to
	// the oldest page looking for it.
	_ = e.st.Discover(context.Background(), "img-gone", state.Asset{Source: "feed-image", FeedItemID: "post-x", FeedItemAt: postTime(25), DiscoveredAt: postTime(26)})
	_ = e.st.MarkError(context.Background(), "img-gone", "boom")
	res := e.run(t, false)
	if r := res.Retry; r.Fixed != 1 || r.NotFound != 1 {
		t.Fatalf("summary = %+v", r)
	}
	// Pages 40..31 and 30..21; the second ends before post 25's date.
	if feed.pages != 2 {
		t.Errorf("feed pages = %d (%v), want 2: stop after the page past the floor", feed.pages, feed.queries)
	}
}

func TestRetryModeMakesNoTagCalls(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, ti := newTagImmich(t)
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)
	ctx := context.Background()
	_ = st.Discover(ctx, "up", state.Asset{Source: "feed-image"})
	_ = st.MarkUploaded(ctx, "up", "asset-9", "created", "", time.Now())
	// A failed entry so the run goes past the nothing-to-retry exit.
	_ = st.Discover(ctx, "bad", state.Asset{Source: "feed-image", DiscoveredAt: time.Now()})
	_ = st.MarkError(ctx, "bad", "boom")
	if len(st.Untagged()) != 1 {
		t.Fatal("setup: want one owed tag")
	}
	tagRun(t, famlySrv, ic, st, true, Options{RetryFailed: true})
	if ti.upserts.Load() != 0 || ti.tagCalls.Load() != 0 {
		t.Errorf("--retry-failed made tag calls: %d/%d", ti.upserts.Load(), ti.tagCalls.Load())
	}
}

func TestRetrySummaryAddsUpAndSeparatesUploadFailures(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"fixed", "upfail", "still", "gone"} {
		_ = st.Discover(ctx, id, state.Asset{Source: "feed-image"})
		_ = st.MarkError(ctx, id, "x")
	}
	p := newRetryPlan(st.Failed())
	for _, id := range []string{"fixed", "upfail", "still"} {
		p.attempted[id] = struct{}{}
	}
	_ = st.MarkSaved(ctx, "fixed", "/p", time.Now())
	_ = st.MarkSaved(ctx, "upfail", "/p", time.Now())
	_ = st.MarkError(ctx, "upfail", "upload: boom") // saved, then the upload failed
	s := p.summary(ctx, st, false)
	if s.Fixed != 1 || s.UploadFailed != 1 || s.StillFailing != 2 || s.NotFound != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if s.Failed != s.Fixed+s.UploadFailed+s.StillFailing {
		t.Errorf("failed %d != fixed+uploadFailed+stillFailing", s.Failed)
	}
}

// seedSavedUndated records images as saved on disk with no post date,
// as entries written before FeedItemAt existed.
func seedSavedUndated(t *testing.T, st *state.Store, ids ...int) {
	t.Helper()
	ctx := context.Background()
	for _, i := range ids {
		id := fmt.Sprintf("img-%d", i)
		if err := st.Discover(ctx, id, state.Asset{Source: "feed-image", FeedItemID: fmt.Sprintf("post-%d", i), DiscoveredAt: postTime(i).Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkSaved(ctx, id, "x/"+id+".jpg", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *retryEnv) fetch(t *testing.T, dry bool, maxPages int) Result {
	t.Helper()
	res, err := Run(context.Background(), e.deps, Options{Source: SourceAll, DryRun: dry, MaxPages: maxPages})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestFetchStampsPostDateOnSavedEntries(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 30, nil, false))
	seedSavedUndated(t, e.st, 5, 25)
	if res := e.fetch(t, false, 0); res.Stamped != 2 {
		t.Errorf("Stamped = %d, want 2", res.Stamped)
	}
	for _, i := range []int{5, 25} {
		got, _ := e.st.Get(context.Background(), fmt.Sprintf("img-%d", i))
		if !got.FeedItemAt.Equal(postTime(i)) {
			t.Errorf("img-%d FeedItemAt = %v, want %v", i, got.FeedItemAt, postTime(i))
		}
	}
}

func TestFetchDryRunLeavesSavedEntriesUndated(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 30, nil, false))
	seedSavedUndated(t, e.st, 5)
	if res := e.fetch(t, true, 0); res.Stamped != 0 {
		t.Errorf("dry-run Stamped = %d, want 0", res.Stamped)
	}
	if got, _ := e.st.Get(context.Background(), "img-5"); !got.FeedItemAt.IsZero() {
		t.Errorf("dry-run stamped FeedItemAt = %v", got.FeedItemAt)
	}
}

func TestStampedDatesPersistAndBoundALaterRetry(t *testing.T) {
	feed := newDatedFeed(t, 100, nil, false)
	e := newRetryEnv(t, feed)
	ctx := context.Background()
	seedSavedUndated(t, e.st, 95)
	e.fetch(t, false, 1)
	if err := e.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(ctx, e.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if got, _ := st.Get(ctx, "img-95"); !got.FeedItemAt.Equal(postTime(95)) {
		t.Fatalf("FeedItemAt after reopen = %v, want %v", got.FeedItemAt, postTime(95))
	}

	// A failed entry now dated: the retry has a floor and jumps.
	e.st, e.deps.State = st, st
	seedFailed(t, st, true, 61)
	if p := newRetryPlan(st.Failed()); p.floor.IsZero() {
		t.Fatal("retry plan has no floor for a dated failed entry")
	}
	feed.pages = 0
	if res := e.run(t, false); res.Retry.Fixed != 1 || feed.pages != 1 {
		t.Errorf("retry = %+v pages = %d, want fixed 1 in 1 page", res.Retry, feed.pages)
	}
}

// seedMissing records a failed entry whose post is not in the feed.
func seedMissing(t *testing.T, st *state.Store, id string, at time.Time) {
	t.Helper()
	a := state.Asset{Source: "feed-image", FeedItemID: "post-x", FeedItemAt: at, DiscoveredAt: postTime(26)}
	if err := st.Discover(context.Background(), id, a); err != nil {
		t.Fatal(err)
	}
	_ = st.MarkError(context.Background(), id, "boom")
}

func isGone(e *retryEnv, id string) bool {
	a, _ := e.st.Get(context.Background(), id)
	return !a.GoneAt.IsZero()
}

func TestRetryGoneDatedMissCoveredByWalkIsMarked(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 100, nil, false))
	seedFailed(t, e.st, true, 40)
	seedMissing(t, e.st, "img-gone", postTime(25))
	res := e.run(t, false)
	if res.Retry.Gone != 1 || !isGone(e, "img-gone") || isGone(e, "img-40") {
		t.Errorf("gone = %d, img-gone %v, summary %+v", res.Retry.Gone, isGone(e, "img-gone"), res.Retry)
	}
}

func TestRetryGoneMissStoppedByMaxPagesIsNotMarked(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 100, nil, false))
	seedFailed(t, e.st, true, 40)
	seedMissing(t, e.st, "img-gone", postTime(5))
	if _, err := Run(context.Background(), e.deps, Options{Source: SourceAll, RetryFailed: true, MaxPages: 1}); err != nil {
		t.Fatal(err)
	}
	if isGone(e, "img-gone") {
		t.Error("marked gone although --max-pages stopped the walk before its date")
	}
}

func TestRetryGoneUndatedMissAtFeedEndIsMarked(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 30, nil, false))
	seedMissing(t, e.st, "img-gone", time.Time{})
	res := e.run(t, false)
	if res.Retry.Gone != 1 || !isGone(e, "img-gone") {
		t.Errorf("summary %+v, gone %v", res.Retry, isGone(e, "img-gone"))
	}
}

func TestRetryGoneUndatedMissStoppedByMaxPagesIsNotMarked(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 100, nil, false))
	seedMissing(t, e.st, "img-gone", time.Time{})
	if _, err := Run(context.Background(), e.deps, Options{Source: SourceAll, RetryFailed: true, MaxPages: 2}); err != nil {
		t.Fatal(err)
	}
	if isGone(e, "img-gone") {
		t.Error("undated miss marked gone before the feed ended")
	}
}

func TestRetryGoneTargetIsSkippedNextRun(t *testing.T) {
	feed := newDatedFeed(t, 100, nil, false)
	e := newRetryEnv(t, feed)
	seedMissing(t, e.st, "img-gone", time.Time{})
	e.run(t, false)
	before := feed.pages
	res := e.run(t, false)
	if feed.pages != before || res.Retry.Failed != 0 {
		t.Errorf("second run read %d pages, failed %d; want a skip", feed.pages-before, res.Retry.Failed)
	}
}

func TestRetryGoneDryRunMarksNothing(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 100, nil, false))
	seedMissing(t, e.st, "img-gone", postTime(25))
	res := e.run(t, true)
	if isGone(e, "img-gone") || res.Retry.Gone != 0 {
		t.Errorf("dry run marked gone: %+v", res.Retry)
	}
}

func TestRetryGoneFlagRevisitsAndClearsOnFind(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 50, nil, false))
	seedFailed(t, e.st, true, 20)
	_ = e.st.MarkGone(context.Background(), "img-20", time.Now())
	if res := e.run(t, false); res.Retry.Failed != 0 {
		t.Fatalf("gone entry revisited without the flag: %+v", res.Retry)
	}
	res, err := Run(context.Background(), e.deps, Options{Source: SourceAll, RetryFailed: true, RetryGone: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Retry.Fixed != 1 || isGone(e, "img-20") {
		t.Errorf("summary %+v, still gone %v", res.Retry, isGone(e, "img-20"))
	}
}

func TestStateWithoutGoneAtRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := `{"img-1":{"source":"feed-image","discoveredAt":"2026-05-01T00:00:00Z","lastError":"x"}}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Failed()) != 1 || len(st.Gone()) != 0 {
		t.Fatalf("failed %d gone %d", len(st.Failed()), len(st.Gone()))
	}
	_ = st.MarkGone(context.Background(), "img-1", time.Now())
	_ = st.Close()
	st, err = state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if len(st.Failed()) != 0 || len(st.Gone()) != 1 {
		t.Errorf("after reopen failed %d gone %d", len(st.Failed()), len(st.Gone()))
	}
}

// cancelAfter cancels a context once the nth feed response is fully
// read, handing the body over from memory so the cancellation cannot
// turn that response into an error.
type cancelAfter struct {
	n      int
	cancel context.CancelFunc
	seen   int
}

func (c *cancelAfter) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil || !strings.HasPrefix(r.URL.Path, "/api/feed/") {
		return resp, err
	}
	c.seen++
	if c.seen == c.n {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(b))
		c.cancel()
	}
	return resp, nil
}

func TestRetryGoneCancelledWalkMarksNothing(t *testing.T) {
	// The context dies as the feed's last (empty) page arrives, so the
	// iterator ends without an error although the walk was cut short.
	feed := newDatedFeed(t, 30, nil, false)
	e := newRetryEnv(t, feed)
	seedMissing(t, e.st, "img-gone", time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hc := &http.Client{Transport: &cancelAfter{n: 4, cancel: cancel}}
	e.deps.Famly = famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(feed.URL), famly.WithPageDelay(0), famly.WithHTTPClient(hc))
	res, err := Run(ctx, e.deps, Options{Source: SourceAll, RetryFailed: true})
	if err != nil || res.PagesWalked != 4 {
		t.Fatalf("err %v, pages %d; want the walk to reach the empty page", err, res.PagesWalked)
	}
	if isGone(e, "img-gone") {
		t.Error("undated miss marked gone after the context was cancelled")
	}
}

func TestRetryGoneFeedErrorMarksNothing(t *testing.T) {
	feed := newDatedFeed(t, 100, nil, false)
	e := newRetryEnv(t, feed)
	seedMissing(t, e.st, "img-gone", time.Time{})
	feed.failFrom = 2
	if _, err := Run(context.Background(), e.deps, Options{Source: SourceAll, RetryFailed: true}); err == nil {
		t.Fatal("want the feed error")
	}
	if isGone(e, "img-gone") {
		t.Error("undated miss marked gone after a feed error")
	}
}

func TestRetryGoneStillAbsentIsRemarkedUnderRetryGone(t *testing.T) {
	e := newRetryEnv(t, newDatedFeed(t, 100, nil, false))
	seedMissing(t, e.st, "img-gone", postTime(25))
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = e.st.MarkGone(context.Background(), "img-gone", old)
	res, err := Run(context.Background(), e.deps, Options{Source: SourceAll, RetryFailed: true, RetryGone: true})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.Get(context.Background(), "img-gone")
	if res.Retry.Gone != 1 || !a.GoneAt.After(old) {
		t.Errorf("gone = %d, goneAt %v (was %v)", res.Retry.Gone, a.GoneAt, old)
	}
}

func TestStateGoneAtKeyAbsentForNeverGoneEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Discover(context.Background(), "img-1", state.Asset{Source: "feed-image", DiscoveredAt: postTime(1)})
	_ = st.MarkError(context.Background(), "img-1", "x")
	_ = st.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "goneAt") {
		t.Errorf("never-gone entry wrote goneAt: %s", b)
	}
}

func TestNormalSightingClearsGoneAt(t *testing.T) {
	ctx := context.Background()
	st, err := state.Open(ctx, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, id := range []string{"a", "b"} {
		_ = st.Discover(ctx, id, state.Asset{Source: "feed-image", DiscoveredAt: postTime(1)})
		_ = st.MarkError(ctx, id, "x")
		_ = st.MarkGone(ctx, id, time.Now())
	}
	_ = st.MarkError(ctx, "a", "failed again")
	_ = st.MarkSaved(ctx, "b", "/p", time.Now())
	for _, id := range []string{"a", "b"} {
		if a, _ := st.Get(ctx, id); !a.GoneAt.IsZero() {
			t.Errorf("%s keeps goneAt after a normal fetch", id)
		}
	}
	if len(st.Gone()) != 0 || len(st.Failed()) != 1 {
		t.Errorf("gone %d failed %d", len(st.Gone()), len(st.Failed()))
	}
}
