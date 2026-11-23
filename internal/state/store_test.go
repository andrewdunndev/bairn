package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDiscoverIdempotent(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()

	if err := s.Discover(ctx, "img-001", Asset{Source: "feed-image", FeedItemID: "post-001"}); err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	if err := s.Discover(ctx, "img-001", Asset{Source: "feed-image", FeedItemID: "different-post"}); err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	got, err := s.Get(ctx, "img-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FeedItemID != "post-001" {
		t.Errorf("FeedItemID = %q, want post-001 (initial value preserved)", got.FeedItemID)
	}
}

func TestLifecycle(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()

	must(t, s.Discover(ctx, "img-001", Asset{Source: "feed-image", FeedItemID: "post-001"}))

	got, err := s.Get(ctx, "img-001")
	if err != nil {
		t.Fatalf("Get after Discover: %v", err)
	}
	if !got.SavedAt.IsZero() || !got.UploadedAt.IsZero() {
		t.Error("times should be zero after Discover")
	}

	now := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)
	must(t, s.MarkDownloaded(ctx, "img-001", "abc123", now))
	must(t, s.MarkSaved(ctx, "img-001", "feed-image-2026-05-06_14-00-00-img-001.jpg", now))
	must(t, s.MarkExifFixed(ctx, "img-001", now, ""))
	must(t, s.MarkUploaded(ctx, "img-001", "asset-001", "created", "", now))

	got, _ = s.Get(ctx, "img-001")
	if got.SavedPath == "" {
		t.Error("SavedPath should be set")
	}
	if got.SHA1 != "abc123" {
		t.Errorf("SHA1 = %q", got.SHA1)
	}
	if got.ImmichAssetID != "asset-001" {
		t.Errorf("ImmichAssetID = %q", got.ImmichAssetID)
	}
	if got.ImmichStatus != "created" {
		t.Errorf("ImmichStatus = %q", got.ImmichStatus)
	}

	saved, err := s.IsSaved(ctx, "img-001")
	if err != nil || !saved {
		t.Errorf("IsSaved = (%v, %v), want (true, nil)", saved, err)
	}
	uploaded, err := s.IsUploaded(ctx, "img-001")
	if err != nil || !uploaded {
		t.Errorf("IsUploaded = (%v, %v), want (true, nil)", uploaded, err)
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// First store: write some state, close, ensure it persists.
	{
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("first Open: %v", err)
		}
		must(t, s.Discover(context.Background(), "a", Asset{Source: "feed-image"}))
		must(t, s.MarkSaved(context.Background(), "a", "a.jpg", time.Now()))
		_ = s.Close()
	}

	// Second store: load, verify state was preserved.
	{
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("second Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		got, err := s.Get(context.Background(), "a")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.SavedPath != "a.jpg" {
			t.Errorf("SavedPath = %q after reopen", got.SavedPath)
		}
	}
}

func TestFlockExcludesSecondOpener(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	second, err := Open(context.Background(), path)
	if err == nil {
		_ = second.Close()
		t.Fatal("expected ErrLocked on second Open while first is held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("expected ErrLocked, got %v", err)
	}

	// Once we close the first, the second can acquire.
	_ = first.Close()
	third, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("third Open after close: %v", err)
	}
	_ = third.Close()
}

func TestNotFoundOnUpdate(t *testing.T) {
	s := tempStore(t)
	err := s.MarkDownloaded(context.Background(), "missing", "x", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStats(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 6, 14, 0, 0, 0, time.UTC)

	must(t, s.Discover(ctx, "a", Asset{Source: "feed-image"}))
	must(t, s.Discover(ctx, "b", Asset{Source: "feed-image"}))
	must(t, s.MarkDownloaded(ctx, "b", "shab", now))
	must(t, s.Discover(ctx, "c", Asset{Source: "feed-image"}))
	must(t, s.MarkDownloaded(ctx, "c", "shac", now))
	must(t, s.MarkSaved(ctx, "c", "c.jpg", now))
	must(t, s.Discover(ctx, "d", Asset{Source: "feed-image"}))
	must(t, s.MarkDownloaded(ctx, "d", "shad", now))
	must(t, s.MarkSaved(ctx, "d", "d.jpg", now))
	must(t, s.MarkUploaded(ctx, "d", "asset-d", "created", "", now))

	sm, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if sm.Total != 4 {
		t.Errorf("Total = %d, want 4", sm.Total)
	}
	if sm.Discovered != 1 {
		t.Errorf("Discovered = %d, want 1", sm.Discovered)
	}
	if sm.Downloaded != 1 {
		t.Errorf("Downloaded = %d, want 1", sm.Downloaded)
	}
	if sm.Saved != 1 {
		t.Errorf("Saved = %d, want 1", sm.Saved)
	}
	if sm.Uploaded != 1 {
		t.Errorf("Uploaded = %d, want 1", sm.Uploaded)
	}
	if !sm.LastUpload.Equal(now) {
		t.Errorf("LastUpload = %v, want %v", sm.LastUpload, now)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestFlushIsBatched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.flushEvery = 3
	ctx := context.Background()
	size := func() int64 {
		fi, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}

	must(t, s.Discover(ctx, "a", Asset{Source: "feed-image"}))
	must(t, s.Discover(ctx, "b", Asset{Source: "feed-image"}))
	if size() != 0 {
		t.Fatal("file written before the batch filled")
	}
	must(t, s.Discover(ctx, "c", Asset{Source: "feed-image"}))
	if size() == 0 {
		t.Fatal("file not written when the batch filled")
	}
	before := size()
	must(t, s.MarkError(ctx, "a", "boom"))
	if size() != before {
		t.Fatal("file rewritten before the next batch filled")
	}
	must(t, s.Flush())
	if size() == before {
		t.Fatal("Flush did not write pending change")
	}
}

func TestCloseFlushesPendingChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	must(t, s.Discover(context.Background(), "a", Asset{Source: "feed-image"}))
	must(t, s.Close())
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if _, err := s2.Get(context.Background(), "a"); err != nil {
		t.Fatalf("pending change lost on Close: %v", err)
	}
}

// State written before upload tracking carried no uploadedAt; those
// entries decode as not uploaded and so get uploaded on the next run.
func TestLegacyEntryDecodesAsNotUploaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"x":{"source":"feed-image","discoveredAt":"2026-01-01T00:00:00Z","savedAt":"2026-01-01T00:00:00Z","savedPath":"x.jpg"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if saved, _ := s.IsSaved(context.Background(), "x"); !saved {
		t.Fatal("legacy entry should be saved")
	}
	if up, _ := s.IsUploaded(context.Background(), "x"); up {
		t.Fatal("legacy entry should not count as uploaded")
	}
	if a, _ := s.Get(context.Background(), "x"); a.UploadSHA1 != "" {
		t.Fatalf("legacy entry UploadSHA1 = %q, want empty", a.UploadSHA1)
	}
}

// The state file is replaced by rename on every flush; the lock must
// outlive that.
func TestLockSurvivesFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	for i := 0; i < DefaultFlushEvery; i++ {
		if err := first.Discover(context.Background(), fmt.Sprintf("a%d", i), Asset{Source: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state not flushed: %v", err)
	}
	second, err := Open(context.Background(), path)
	if err == nil {
		_ = second.Close()
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open after a flush: err = %v, want ErrLocked", err)
	}
}

func TestOpenLeavesUserDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 755 untouched", fi.Mode().Perm())
	}
}

// legacyStore opens a state file shaped like one written before post
// dates and album filing were tracked.
func legacyStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	const legacy = `{
  "old-1": {"source":"feed-image","discoveredAt":"2026-01-02T00:00:00Z","downloadedAt":"2026-01-31T23:30:00Z","uploadedAt":"2026-02-01T08:00:00Z","immichAssetId":"im-1"},
  "old-noid": {"source":"feed-image","discoveredAt":"2026-01-02T00:00:00Z","downloadedAt":"2026-01-05T10:00:00Z","uploadedAt":"2026-02-01T08:00:00Z"},
  "old-nodate": {"source":"feed-image","discoveredAt":"2026-01-02T00:00:00Z","uploadedAt":"2026-02-01T08:00:00Z","immichAssetId":"im-3"},
  "not-uploaded": {"source":"feed-image","discoveredAt":"2026-01-02T00:00:00Z","downloadedAt":"2026-01-05T10:00:00Z","immichAssetId":"im-4"}
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUnfiledFallsBackToDownloadedAtForLegacyEntries(t *testing.T) {
	s := legacyStore(t)
	got := s.Unfiled()
	if len(got) != 1 || got[0].ID != "old-1" || got[0].ImmichAssetID != "im-1" {
		t.Fatalf("Unfiled = %+v, want only old-1", got)
	}
	want := time.Date(2026, 1, 31, 23, 30, 0, 0, time.UTC)
	if !got[0].PostedAt.Equal(want) {
		t.Errorf("PostedAt = %v, want DownloadedAt %v", got[0].PostedAt, want)
	}
	// Month boundary: UTC January 31 is already February in a zone east of UTC.
	if m := got[0].PostedAt.In(time.FixedZone("e", 2*3600)).Month(); m != time.February {
		t.Errorf("month in +02:00 = %v, want February", m)
	}
}

func TestLegacyEntryIsFiledOnceAndNeverRelisted(t *testing.T) {
	s := legacyStore(t)
	ctx := context.Background()
	u := s.Unfiled()[0]
	must(t, s.MarkPosted(ctx, u.ID, u.PostedAt))
	must(t, s.MarkAlbumsFiled(ctx, u.ID, time.Now()))
	if got := s.Unfiled(); len(got) != 0 {
		t.Fatalf("after filing, Unfiled = %+v, want none", got)
	}
	a, _ := s.Get(ctx, "old-noid")
	if !a.PostedAt.IsZero() || a.ImmichAssetID != "" {
		t.Errorf("entry without an Immich asset id was touched: %+v", a)
	}
}

func TestRefusedAssetIsNeverRelisted(t *testing.T) {
	s := legacyStore(t)
	ctx := context.Background()
	must(t, s.MarkAlbumsRefused(ctx, "old-1", "no_permission"))
	if got := s.Unfiled(); len(got) != 0 {
		t.Fatalf("Unfiled = %+v, want none after refusal", got)
	}
	sm, _ := s.Stats(ctx)
	if sm.AlbumsRefused != 1 || sm.AlbumsUnfiled != 0 || sm.AlbumsFiled != 0 {
		t.Errorf("summary = %+v", sm)
	}
}

func TestStatsCountsAlbumState(t *testing.T) {
	s := legacyStore(t)
	ctx := context.Background()
	sm, _ := s.Stats(ctx)
	if sm.AlbumsUnfiled != 1 || sm.AlbumsFiled != 0 {
		t.Fatalf("before: %+v", sm)
	}
	must(t, s.MarkAlbumsFiled(ctx, "old-1", time.Now()))
	sm, _ = s.Stats(ctx)
	if sm.AlbumsUnfiled != 0 || sm.AlbumsFiled != 1 {
		t.Errorf("after: %+v", sm)
	}
}

func TestUntaggedListsUploadsOwedTheTagOnce(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	for _, id := range []string{"old", "new", "refused", "saved-only", "noid"} {
		_ = st.Discover(ctx, id, Asset{Source: "feed-image"})
	}
	_ = st.MarkSaved(ctx, "saved-only", "/p", now)
	for _, id := range []string{"old", "new", "refused"} {
		_ = st.MarkUploaded(ctx, id, "imm-"+id, "created", "", now)
	}
	_ = st.MarkUploaded(ctx, "noid", "", "created", "", now)
	_ = st.MarkTagged(ctx, "new", now)
	_ = st.MarkTagRefused(ctx, "refused", "no_permission")

	got := st.Untagged()
	if len(got) != 1 || got[0].ID != "old" || got[0].ImmichAssetID != "imm-old" {
		t.Fatalf("Untagged = %+v, want only old", got)
	}
	sm, _ := st.Stats(ctx)
	if sm.TagsApplied != 1 || sm.TagsOwed != 1 || sm.TagsRefused != 1 {
		t.Errorf("stats = %+v", sm)
	}
}

func TestFailedExcludesAnEntryThatWasSaved(t *testing.T) {
	s := tempStore(t)
	ctx := context.Background()
	_ = s.Discover(ctx, "a", Asset{Source: "feed-image"})
	_ = s.MarkSaved(ctx, "a", "/p", time.Now())
	// Saved, then the upload failed: LastError and SavedAt both set.
	_ = s.MarkError(ctx, "a", "upload: boom")
	if got := s.Failed(); len(got) != 0 {
		t.Errorf("Failed() = %+v, want a saved entry left out (--retry-failed is for unsaved ones)", got)
	}
}

// A legacy entry survives load, flush and reload without gaining an
// uploadSha1 key; a populated uploadSha1 survives the same trip.
func TestUploadSHA1FlushRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"x":{"source":"feed-image","discoveredAt":"2026-01-01T00:00:00Z","savedAt":"2026-01-01T00:00:00Z","savedPath":"x.jpg"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	must(t, s.Discover(ctx, "y", Asset{Source: "feed-image"}))
	must(t, s.MarkUploaded(ctx, "y", "imm-y", "created", "abc123", time.Now()))
	must(t, s.Close())

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["x"]["uploadSha1"]; ok {
		t.Errorf("legacy entry gained an uploadSha1 key: %s", raw)
	}
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if a, _ := s2.Get(ctx, "x"); a.UploadSHA1 != "" {
		t.Errorf("legacy UploadSHA1 = %q", a.UploadSHA1)
	}
	if a, _ := s2.Get(ctx, "y"); a.UploadSHA1 != "abc123" {
		t.Errorf("UploadSHA1 = %q after reload, want abc123", a.UploadSHA1)
	}
}

func TestStampDefersItsFlushToCloseAndKeepsDates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.flushEvery = 2
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	ids := []string{"a", "b", "c", "d"} // more than flushEvery
	for _, id := range ids {
		must(t, s.Discover(ctx, id, Asset{Source: "feed-image"}))
	}
	must(t, s.Flush())
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		must(t, s.Discover(ctx, id, Asset{FeedItemAt: at}))
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
		t.Fatal("stamps rewrote the file before Close")
	}
	must(t, s.Close())
	r, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	for _, id := range ids {
		if got, _ := r.Get(ctx, id); !got.FeedItemAt.Equal(at) {
			t.Fatalf("%s FeedItemAt after reopen = %v, want %v", id, got.FeedItemAt, at)
		}
	}
}

func TestDiscoverKeepsExistingFeedItemAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.flushEvery = 1
	first := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	must(t, s.Discover(ctx, "a", Asset{FeedItemAt: first}))
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dirty := s.dirty
	must(t, s.Discover(ctx, "a", Asset{FeedItemAt: first.AddDate(0, 1, 0)}))
	if got, _ := s.Get(ctx, "a"); !got.FeedItemAt.Equal(first) {
		t.Fatalf("FeedItemAt = %v, want unchanged %v", got.FeedItemAt, first)
	}
	if s.dirty != dirty {
		t.Fatalf("dirty = %d, want %d", s.dirty, dirty)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
		t.Fatal("Discover on a dated entry wrote the file")
	}
	must(t, s.Close())
}

func TestStampUploadSHA1NeverOverwrites(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_ = s.Discover(ctx, "a", Asset{Source: "feed_image"})
	_ = s.MarkUploaded(ctx, "a", "i", "created", "", time.Now())
	if !s.StampUploadSHA1("a", "first") {
		t.Fatal("blank value not stamped")
	}
	if s.StampUploadSHA1("a", "second") {
		t.Error("stamped over a set value")
	}
	if a, _ := s.Get(ctx, "a"); a.UploadSHA1 != "first" {
		t.Errorf("UploadSHA1 = %q, want first", a.UploadSHA1)
	}
}
