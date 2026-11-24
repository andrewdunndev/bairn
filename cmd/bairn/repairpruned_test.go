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

// seedPrunedRepair stores three pruned entries: "here" with its file on
// disk, "gone" marked gone by Famly, "lost" with neither; plus one
// saved entry that is not pruned.
func seedPrunedRepair(t *testing.T) (st *state.Store, path, dir string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "state.json")
	st, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"here", "gone", "lost"} {
		addPruned(t, st, id, dir)
	}
	if err := os.WriteFile(filepath.Join(dir, "here.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = st.Discover(context.Background(), "plain", state.Asset{Source: "feed-image",
		SavedAt: time.Now(), SavedPath: filepath.Join(dir, "plain.jpg")})
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	return st, path, dir
}

func withGone(t *testing.T, st *state.Store, id string) {
	t.Helper()
	// MarkGone needs a failed entry; the entry keeps its tombstone.
	if err := st.MarkGone(context.Background(), id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestRepairPrunedThreeClasses(t *testing.T) {
	st, _, _ := seedPrunedRepair(t)
	defer st.Close()
	withGone(t, st, "gone")
	got := repairPruned(st, nil, false, quiet)
	if want := (prunedSummary{Considered: 3, Unpruned: 1, Requeued: 1, NotRestorable: 1}); got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	ctx := context.Background()
	here, _ := st.Get(ctx, "here")
	if !here.PrunedAt.IsZero() || here.SavedAt.IsZero() || here.LastError != "" {
		t.Errorf("here: %+v", here)
	}
	gone, _ := st.Get(ctx, "gone")
	if gone.PrunedAt.IsZero() || gone.GoneAt.IsZero() {
		t.Errorf("gone touched: %+v", gone)
	}
	lost, _ := st.Get(ctx, "lost")
	if !lost.PrunedAt.IsZero() || !lost.SavedAt.IsZero() || lost.SavedPath != "" ||
		lost.LastError != prunedRestoreError || lost.ImmichAssetID != "imm-lost" || lost.UploadSHA1 != "up-lost" {
		t.Errorf("lost: %+v", lost)
	}
	if f := st.Failed(); len(f) != 1 || f[0].ID != "lost" {
		t.Errorf("Failed = %+v, want only lost", f)
	}
}

func TestRepairPrunedDryRunIsByteIdentical(t *testing.T) {
	st, path, _ := seedPrunedRepair(t)
	withGone(t, st, "gone")
	before, _ := os.ReadFile(path)
	got := repairPruned(st, nil, true, quiet)
	if want := (prunedSummary{Considered: 3, Unpruned: 1, Requeued: 1, NotRestorable: 1}); got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("dry run changed state.json")
	}
}

func TestRepairPrunedIDsAndBadIDs(t *testing.T) {
	st, _, _ := seedPrunedRepair(t)
	defer st.Close()
	got := repairPruned(st, []string{"lost", "plain", "nope", "lost"}, false, quiet)
	if want := (prunedSummary{Considered: 1, Requeued: 1, Errors: 2}); got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	if a, _ := st.Get(context.Background(), "here"); a.PrunedAt.IsZero() {
		t.Error("unselected entry touched")
	}
}

func TestRunRepairPrunedExitCodes(t *testing.T) {
	st, path, dir := seedPrunedRepair(t)
	_ = st.Close()
	cfg := &config.Config{StatePath: path, SaveDir: dir}
	if code := runRepair(context.Background(), cfg, quiet, []string{"pruned", "--dry-run"}); code != 0 {
		t.Errorf("dry run exit %d", code)
	}
	if code := runRepair(context.Background(), cfg, quiet, []string{"pruned", "nope"}); code != 1 {
		t.Errorf("bad id exit %d, want 1", code)
	}
	if code := runRepair(context.Background(), cfg, quiet, []string{"pruned"}); code != 0 {
		t.Errorf("exit %d", code)
	}
	st, _ = state.Open(context.Background(), path)
	defer st.Close()
	if f := st.Failed(); len(f) != 2 {
		t.Errorf("Failed = %+v, want gone and lost", f)
	}
}
