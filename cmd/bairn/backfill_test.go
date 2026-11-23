package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

func sha(b []byte) string { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }

// seed writes a state file with entries uploaded at t0, then opens it.
func seedBackfill(t *testing.T) (st *state.Store, statePath, dir string, t0 time.Time) {
	t.Helper()
	dir = t.TempDir()
	statePath = filepath.Join(dir, "state.json")
	t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	s, err := state.Open(ctx, statePath)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, body string, mtime time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	add := func(id, path string, exifAt time.Time, upSHA string) {
		_ = s.Discover(ctx, id, state.Asset{Source: "feed_image"})
		_ = s.MarkSaved(ctx, id, path, t0)
		_ = s.MarkExifFixed(ctx, id, exifAt, "")
		_ = s.MarkUploaded(ctx, id, "imm-"+id, "created", upSHA, t0)
	}
	add("ok", write("ok.jpg", "alpha", t0.Add(-time.Hour)), t0, "")
	add("mtime", write("mtime.jpg", "beta", t0.Add(time.Hour)), t0, "")
	add("exif", write("exif.jpg", "gamma", t0.Add(-time.Hour)), t0.Add(time.Minute), "")
	add("gone", filepath.Join(dir, "gone.jpg"), t0, "")
	add("set", write("set.jpg", "delta", t0.Add(-time.Hour)), t0, "preset")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	return s, statePath, dir, t0
}

func TestBackfillUploadSHA1(t *testing.T) {
	st, _, _, _ := seedBackfill(t)
	defer st.Close()
	got := backfillUploadSHA1(st, false, quiet)
	want := backfillSummary{Considered: 4, Hashed: 1, SkippedModified: 2, MissingFile: 1}
	if got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	ctx := context.Background()
	check := func(id, sum string) {
		a, _ := st.Get(ctx, id)
		if a.UploadSHA1 != sum {
			t.Errorf("%s: UploadSHA1 = %q, want %q", id, a.UploadSHA1, sum)
		}
	}
	check("ok", sha([]byte("alpha")))
	check("mtime", "")
	check("exif", "")
	check("gone", "")
	check("set", "preset")
}

func TestBackfillDryRunWritesNothing(t *testing.T) {
	st, path, _, _ := seedBackfill(t)
	before, _ := os.ReadFile(path)
	got := backfillUploadSHA1(st, true, quiet)
	if got.Hashed != 1 {
		t.Fatalf("hashed = %d, want 1", got.Hashed)
	}
	if a, _ := st.Get(context.Background(), "ok"); a.UploadSHA1 != "" {
		t.Error("dry run stamped memory")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("dry run changed the state file")
	}
}

func TestBackfillFlushesOnceAndPersists(t *testing.T) {
	st, path, _, _ := seedBackfill(t)
	before, _ := os.ReadFile(path)
	backfillUploadSHA1(st, false, quiet)
	mid, _ := os.ReadFile(path)
	if string(before) != string(mid) {
		t.Error("state file rewritten before the deferred flush")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	re, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if a, _ := re.Get(context.Background(), "ok"); a.UploadSHA1 != sha([]byte("alpha")) {
		t.Errorf("stamp not persisted: %q", a.UploadSHA1)
	}
}

func TestBackfillHoldsStateLock(t *testing.T) {
	st, path, _, _ := seedBackfill(t)
	defer st.Close()
	// st holds the lock, as a running fetch would; the command must refuse.
	code := runBackfill(context.Background(), &config.Config{StatePath: path}, quiet, []string{"upload-sha1"})
	if code != 1 {
		t.Errorf("exit = %d, want 1 while the state is locked", code)
	}
}

func TestBackfillExitsNonzeroOnErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	ctx := context.Background()
	s, _ := state.Open(ctx, path)
	_ = s.Discover(ctx, "dir", state.Asset{Source: "feed_image"})
	_ = s.MarkSaved(ctx, "dir", dir, time.Unix(1, 0))
	_ = s.MarkUploaded(ctx, "dir", "i", "created", "", time.Now())
	_ = s.Close()
	code := runBackfill(ctx, &config.Config{StatePath: path}, quiet, []string{"upload-sha1"})
	if code != 1 {
		t.Errorf("exit = %d, want 1 for a non-regular SavedPath", code)
	}
}
