package sync

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// seedPruned records img-<i> as saved, uploaded and pruned: the file is
// gone from disk, every other field kept.
func seedPruned(t *testing.T, st *state.Store, i int, lastErr string) {
	t.Helper()
	id := fmt.Sprintf("img-%d", i)
	at := postTime(i)
	err := st.Discover(context.Background(), id, state.Asset{
		Source: "feed-image", FeedItemID: fmt.Sprintf("post-%d", i), FeedItemAt: at, DiscoveredAt: at,
		DownloadedAt: at, SavedAt: at, SavedPath: "/gone/" + id + ".jpg", SHA1: "sha-" + id,
		UploadSHA1: "up-" + id, UploadedAt: at, ImmichAssetID: "imm-" + id, ImmichStatus: "created",
		VerifiedAt: at, PrunedAt: at, LastError: lastErr,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// prunedEnv wires an Immich fake that counts uploads and an empty disk
// sink, so a re-download or re-upload shows as a count or a file.
func prunedEnv(t *testing.T, n int) (*retryEnv, *atomic.Int32) {
	t.Helper()
	e := newRetryEnv(t, newDatedFeed(t, n, nil, false))
	var uploads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"new","status":"created"}`))
	}))
	t.Cleanup(srv.Close)
	e.deps.Immich = sink.NewImmich(immich.New(srv.URL, "k"))
	return e, &uploads
}

func filesUnder(t *testing.T, dir string) (n int) {
	t.Helper()
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func assertUntouched(t *testing.T, e *retryEnv, uploads *atomic.Int32, id string, want state.Asset) {
	t.Helper()
	if n := filesUnder(t, e.dir); n != 0 {
		t.Errorf("disk sink wrote %d files, want 0 (pruned entry re-downloaded)", n)
	}
	if n := uploads.Load(); n != 0 {
		t.Errorf("%d uploads, want 0", n)
	}
	got, _ := e.st.Get(context.Background(), id)
	if got != want {
		t.Errorf("entry changed:\n got %+v\nwant %+v", got, want)
	}
}

func TestFetchNeverRedownloadsOrReuploadsPruned(t *testing.T) {
	e, uploads := prunedEnv(t, 3)
	seedPruned(t, e.st, 1, "")
	want, _ := e.st.Get(context.Background(), "img-1")
	res := e.fetch(t, false, 0)
	if res.Saved != 2 {
		t.Errorf("Saved = %d, want 2 (the two unpruned images only)", res.Saved)
	}
	if n := filesUnder(t, e.dir); n != 2 {
		t.Errorf("disk has %d files, want 2", n)
	}
	if n := uploads.Load(); n != 2 {
		t.Errorf("uploads = %d, want 2", n)
	}
	got, _ := e.st.Get(context.Background(), "img-1")
	if got != want {
		t.Errorf("pruned entry changed:\n got %+v\nwant %+v", got, want)
	}
}

// A pruned entry that also carries a stale LastError (a repair run
// before the guards, say) must not be revisited by --retry-failed. A pin:
// Failed already requires a zero SavedAt, which a pruned entry keeps.
func TestRetryFailedNeverRedownloadsPruned(t *testing.T) {
	e, uploads := prunedEnv(t, 3)
	seedPruned(t, e.st, 1, "saved file missing")
	want, _ := e.st.Get(context.Background(), "img-1")
	if f := e.st.Failed(); len(f) != 0 {
		t.Fatalf("Failed lists a pruned entry: %+v", f)
	}
	e.run(t, false)
	assertUntouched(t, e, uploads, "img-1", want)
}

func TestRequeueRefusesPrunedThenRetryFailedLeavesIt(t *testing.T) {
	e, uploads := prunedEnv(t, 3)
	seedPruned(t, e.st, 1, "")
	want, _ := e.st.Get(context.Background(), "img-1")
	if n := e.st.Requeue([]string{"img-1"}, "saved file missing"); n != 0 {
		t.Fatalf("Requeue changed %d, want 0", n)
	}
	e.run(t, false)
	assertUntouched(t, e, uploads, "img-1", want)
}
