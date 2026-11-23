package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// seedRepair stores one saved entry with its file, one saved entry
// whose file is gone, and one entry that never saved.
func seedRepair(t *testing.T) (st *state.Store, path, dir string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "state.json")
	ctx := context.Background()
	st, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	present := filepath.Join(dir, "present.jpg")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for id, p := range map[string]string{"present": present, "gone": filepath.Join(dir, "gone.jpg")} {
		_ = st.Discover(ctx, id, state.Asset{Source: "feed-image", FeedItemID: "post-" + id, FeedItemAt: at})
		_ = st.MarkDownloaded(ctx, id, "sha-"+id, at)
		_ = st.MarkSaved(ctx, id, p, at)
		_ = st.MarkUploaded(ctx, id, "imm-"+id, "created", "up-"+id, at)
	}
	_ = st.Discover(ctx, "fresh", state.Asset{Source: "feed-image"})
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	return st, path, dir
}

func TestRepairMarksOnlyMissingEntries(t *testing.T) {
	st, _, _ := seedRepair(t)
	defer st.Close()
	got := repairMissing(st, false, quiet)
	if want := (repairSummary{Considered: 2, Marked: 1, Present: 1}); got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	ctx := context.Background()
	if f := st.Failed(); len(f) != 1 || f[0].ID != "gone" {
		t.Fatalf("Failed = %+v, want only gone", f)
	}
	if ok, _ := st.IsSaved(ctx, "gone"); ok {
		t.Error("repaired entry still counts as saved")
	}
	a, _ := st.Get(ctx, "present")
	if a.SavedAt.IsZero() || a.SavedPath == "" || a.LastError != "" {
		t.Errorf("present entry touched: %+v", a)
	}
	if a, _ := st.Get(ctx, "fresh"); a.LastError != "" {
		t.Errorf("unsaved entry touched: %+v", a)
	}
}

func TestRepairKeepsTheOtherFields(t *testing.T) {
	st, _, _ := seedRepair(t)
	defer st.Close()
	ctx := context.Background()
	before, _ := st.Get(ctx, "gone")
	repairMissing(st, false, quiet)
	after, _ := st.Get(ctx, "gone")
	if after.LastError != "saved file missing" || !after.SavedAt.IsZero() || after.SavedPath != "" {
		t.Errorf("not requeued: %+v", after)
	}
	// Normalise the three fields repair owns, then everything else must match.
	after.LastError, after.SavedAt, after.SavedPath = before.LastError, before.SavedAt, before.SavedPath
	if after != before {
		t.Errorf("repair changed other fields:\n before %+v\n after  %+v", before, after)
	}
	if after.ImmichAssetID != "imm-gone" || after.UploadSHA1 != "up-gone" || after.FeedItemAt.IsZero() {
		t.Errorf("kept fields lost: %+v", after)
	}
}

func TestRepairDryRunWritesNothing(t *testing.T) {
	st, path, _ := seedRepair(t)
	before, _ := os.ReadFile(path)
	got := repairMissing(st, true, quiet)
	if got.Marked != 1 {
		t.Fatalf("marked = %d, want 1 counted", got.Marked)
	}
	if a, _ := st.Get(context.Background(), "gone"); a.LastError != "" || a.SavedAt.IsZero() {
		t.Error("dry run changed memory")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("dry run changed the state file")
	}
}

func TestRepairPersistsAndIsIdempotent(t *testing.T) {
	st, path, _ := seedRepair(t)
	repairMissing(st, false, quiet)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	re, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if f := re.Failed(); len(f) != 1 {
		t.Fatalf("not persisted: %+v", f)
	}
	if got := repairMissing(re, false, quiet); got.Marked != 0 || got.Considered != 1 {
		t.Errorf("second run = %+v, want nothing marked", got)
	}
}

func TestRepairUsageAndLock(t *testing.T) {
	st, path, _ := seedRepair(t)
	defer st.Close()
	cfg := &config.Config{StatePath: path}
	ctx := context.Background()
	if code := runRepair(ctx, cfg, quiet, nil); code != 2 {
		t.Errorf("no args exit = %d, want 2", code)
	}
	if code := runRepair(ctx, cfg, quiet, []string{"present"}); code != 2 {
		t.Errorf("bad arg exit = %d, want 2", code)
	}
	if code := runRepair(ctx, cfg, quiet, []string{"missing"}); code != 1 {
		t.Errorf("locked exit = %d, want 1", code)
	}
}

func TestRepairExitsNonzeroOnErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	ctx := context.Background()
	s, _ := state.Open(ctx, path)
	_ = s.Discover(ctx, "dir", state.Asset{Source: "feed-image"})
	_ = s.MarkSaved(ctx, "dir", dir, time.Unix(1, 0))
	_ = s.Close()
	if code := runRepair(ctx, &config.Config{StatePath: path}, quiet, []string{"missing"}); code != 1 {
		t.Errorf("exit = %d, want 1 for a non-regular SavedPath", code)
	}
}
