package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const staleDownloadErr = `asset: download img-1: Get "https://x/y": read tcp 10.0.0.1:1->10.0.0.2:443: connection reset by peer`

func staleAsset(savedAt, uploadedAt time.Time, lastError string) Asset {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	a := Asset{Source: "feed-image", DiscoveredAt: at, DownloadedAt: at, SHA1: "s", Retries: 1, LastError: lastError}
	if !savedAt.IsZero() {
		a.SavedAt, a.SavedPath = savedAt, "/disk/a.jpg"
	}
	if !uploadedAt.IsZero() {
		a.UploadedAt, a.ImmichAssetID = uploadedAt, "imm-1"
	}
	return a
}

func TestMarkUploadedClearsLastErrorOnSavedEntry(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s := tempStore(t)
	if err := s.Discover(ctx, "a", staleAsset(at, time.Time{}, "asset: upload a: boom")); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUploaded(ctx, "a", "imm-1", "created", "up", at); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Get(ctx, "a")
	if a.LastError != "" || a.Retries != 1 {
		t.Fatalf("LastError=%q Retries=%d, want cleared and 1", a.LastError, a.Retries)
	}
}

func TestMarkUploadedKeepsErrorOnUnsavedEntry(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	s := tempStore(t)
	if err := s.Discover(ctx, "a", staleAsset(time.Time{}, time.Time{}, "missing")); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkUploaded(ctx, "a", "imm-1", "created", "up", at); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Get(ctx, "a"); a.LastError == "" {
		t.Fatal("an unsaved entry is still failed; its error must stay")
	}
}

func TestOpenClearsStaleErrorOnSavedUploadedEntries(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for id, a := range map[string]Asset{
		"stale":    staleAsset(at, at, staleDownloadErr),
		"unsent":   staleAsset(at, time.Time{}, "asset: upload unsent: boom"),
		"requeued": staleAsset(time.Time{}, at, "file missing"),
		"failed":   staleAsset(time.Time{}, time.Time{}, staleDownloadErr),
	} {
		if err := s.Discover(ctx, id, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := map[string]bool{"stale": false, "unsent": true, "requeued": true, "failed": true}
	for id, keep := range want {
		a, _ := s.Get(ctx, id)
		if (a.LastError != "") != keep {
			t.Errorf("%s: LastError=%q, kept=%v want %v", id, a.LastError, a.LastError != "", keep)
		}
		if a.Retries != 1 {
			t.Errorf("%s: Retries=%d, want 1", id, a.Retries)
		}
	}
	if sm, _ := s.Stats(ctx); sm.WithErrors != 3 {
		t.Errorf("WithErrors=%d, want 3", sm.WithErrors)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
