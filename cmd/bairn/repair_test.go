package main

import (
	"context"
	"fmt"
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
	st, _, dir := seedRepair(t)
	defer st.Close()
	got, _ := repairMissing(st, dir, false, quiet)
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
	st, _, dir := seedRepair(t)
	defer st.Close()
	ctx := context.Background()
	before, _ := st.Get(ctx, "gone")
	repairMissing(st, dir, false, quiet)
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
	st, path, dir := seedRepair(t)
	before, _ := os.ReadFile(path)
	got, _ := repairMissing(st, dir, true, quiet)
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
	st, path, dir := seedRepair(t)
	repairMissing(st, dir, false, quiet)
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
	if got, _ := repairMissing(re, dir, false, quiet); got.Marked != 0 || got.Considered != 1 {
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
	if code := runRepair(ctx, &config.Config{StatePath: path, SaveDir: dir}, quiet, []string{"missing"}); code != 1 {
		t.Errorf("exit = %d, want 1 for a non-regular SavedPath", code)
	}
}

// seedMany stores n saved entries whose files are absent under dir.
func seedMany(t *testing.T, n int, present int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	ctx := context.Background()
	s, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		id := fmt.Sprintf("e%d", i)
		p := filepath.Join(dir, id+".jpg")
		if i < present {
			_ = os.WriteFile(p, []byte("x"), 0o600)
		}
		_ = s.Discover(ctx, id, state.Asset{Source: "feed-image"})
		_ = s.MarkSaved(ctx, id, p, time.Unix(1, 0))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func TestRepairRefusesBadSaveRoot(t *testing.T) {
	dir, path := seedMany(t, 2, 1)
	before, _ := os.ReadFile(path)
	ctx := context.Background()
	file := filepath.Join(dir, "e0.jpg")
	for name, root := range map[string]string{"absent": filepath.Join(dir, "nope"), "file": file, "unset": ""} {
		cfg := &config.Config{StatePath: path, SaveDir: root}
		if code := runRepair(ctx, cfg, quiet, []string{"missing"}); code != 1 {
			t.Errorf("%s root: exit = %d, want 1", name, code)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a refused repair wrote the state file")
	}
}

func TestRepairRefusesWhenEverythingIsMissing(t *testing.T) {
	dir, path := seedMany(t, mountGoneMin+1, 0)
	before, _ := os.ReadFile(path)
	cfg := &config.Config{StatePath: path, SaveDir: dir}
	if code := runRepair(context.Background(), cfg, quiet, []string{"missing"}); code != 1 {
		t.Fatalf("exit = %d, want 1 when all files are missing", code)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a refused repair wrote the state file")
	}
	// A few entries, all missing, is a small library and goes through.
	dir2, path2 := seedMany(t, mountGoneMin, 0)
	if code := runRepair(context.Background(), &config.Config{StatePath: path2, SaveDir: dir2}, quiet, []string{"missing"}); code != 0 {
		t.Errorf("small all-missing exit = %d, want 0", code)
	}
	// One present file means the mount is up.
	dir3, path3 := seedMany(t, mountGoneMin+2, 1)
	if code := runRepair(context.Background(), &config.Config{StatePath: path3, SaveDir: dir3}, quiet, []string{"missing"}); code != 0 {
		t.Errorf("mixed exit = %d, want 0", code)
	}
}

func TestRepairLeavesStatErrorEntriesAlone(t *testing.T) {
	st, _, dir := seedRepair(t)
	defer st.Close()
	ctx := context.Background()
	// A directory as SavedPath is a stat error that is not absence.
	_ = st.Discover(ctx, "odd", state.Asset{Source: "feed-image"})
	_ = st.MarkSaved(ctx, "odd", dir, time.Unix(1, 0))
	before, _ := st.Get(ctx, "odd")
	got, err := repairMissing(st, dir, false, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if got.Errors != 1 || got.Marked != 1 {
		t.Fatalf("summary = %+v, want 1 error and only gone marked", got)
	}
	after, _ := st.Get(ctx, "odd")
	if after != before {
		t.Errorf("stat-error entry changed:\n before %+v\n after  %+v", before, after)
	}
	if ok, _ := st.IsSaved(ctx, "odd"); !ok {
		t.Error("stat-error entry no longer saved")
	}
	for _, f := range st.Failed() {
		if f.ID == "odd" {
			t.Error("stat-error entry listed as failed")
		}
	}
}
