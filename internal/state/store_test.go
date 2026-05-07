package state

import (
	"context"
	"errors"
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
	must(t, s.MarkUploaded(ctx, "img-001", "asset-001", "created", now))

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
	must(t, s.MarkUploaded(ctx, "d", "asset-d", "created", now))

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
