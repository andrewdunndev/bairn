package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/state"
)

// addPruned stores a verified, pruned entry whose file is gone from disk.
func addPruned(t *testing.T, st *state.Store, id, dir string) {
	t.Helper()
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	err := st.Discover(context.Background(), id, state.Asset{
		Source: "feed-image", FeedItemID: "post-" + id, FeedItemAt: at, DiscoveredAt: at,
		DownloadedAt: at, SavedAt: at, SavedPath: filepath.Join(dir, id+".jpg"), SHA1: "sha-" + id,
		UploadSHA1: "up-" + id, UploadedAt: at, ImmichAssetID: "imm-" + id,
		ImmichVerifiedAt: at, PrunedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRepairNeverRequeuesPruned(t *testing.T) {
	st, _, dir := seedRepair(t)
	defer st.Close()
	addPruned(t, st, "pruned", dir)
	got, _ := repairMissing(st, dir, false, quiet)
	if want := (repairSummary{Considered: 2, Marked: 1, Present: 1}); got != want {
		t.Fatalf("summary = %+v, want %+v (pruned entry must not be considered)", got, want)
	}
	if f := st.Failed(); len(f) != 1 || f[0].ID != "gone" {
		t.Fatalf("Failed = %+v, want only gone", f)
	}
	if a, _ := st.Get(context.Background(), "pruned"); a.SavedAt.IsZero() || a.LastError != "" {
		t.Errorf("pruned entry requeued: %+v", a)
	}
}

// With only pruned files absent, repair has nothing to do and must not
// read the empty disk as a gone mount.
func TestRepairWithOnlyPrunedAbsentIsANoop(t *testing.T) {
	st, _, dir := seedRepair(t)
	defer st.Close()
	_ = st.Requeue([]string{"gone"}, "x")
	addPruned(t, st, "pruned", dir)
	got, err := repairMissing(st, dir, false, quiet)
	if err != nil || got.Marked != 0 {
		t.Errorf("summary = %+v err = %v, want nothing marked", got, err)
	}
}

func seedStatusPruned(t *testing.T) string {
	t.Helper()
	path := seedStatus(t)
	st, err := state.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	addPruned(t, st, "pruned-1", filepath.Dir(path))
	addPruned(t, st, "pruned-2", filepath.Dir(path))
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStatusNeverCountsPrunedAsMissing(t *testing.T) {
	path := seedStatusPruned(t)
	out, code := runStatusCapture(t, path)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got struct{ MissingFile, Pruned, Verified, VerifyFailed int }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	// seedStatus has 3 missing (gone, blank, dir); the 2 pruned add none.
	if got.MissingFile != 3 || got.Pruned != 2 || got.Verified != 2 || got.VerifyFailed != 0 {
		t.Errorf("missingFile=%d pruned=%d verified=%d verifyFailed=%d, want 3 2 2 0", got.MissingFile, got.Pruned, got.Verified, got.VerifyFailed)
	}
	list, _ := runStatusCapture(t, path, "--missing")
	if strings.Contains(list, "pruned") {
		t.Errorf("--missing lists a pruned entry: %q", list)
	}
}

func TestBackfillIgnoresPruned(t *testing.T) {
	st, _, dir, _ := seedBackfill(t)
	defer st.Close()
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	_ = st.Discover(context.Background(), "pruned", state.Asset{Source: "feed_image", SavedAt: at,
		SavedPath: filepath.Join(dir, "pruned.jpg"), UploadedAt: at, ImmichAssetID: "imm-pruned", PrunedAt: at})
	got := backfillUploadSHA1(st, false, quiet)
	want := backfillSummary{Considered: 4, Hashed: 1, SkippedModified: 2, MissingFile: 1}
	if got != want {
		t.Fatalf("summary = %+v, want %+v (pruned entry counted)", got, want)
	}
	if a, _ := st.Get(context.Background(), "pruned"); a.UploadSHA1 != "" || a.PrunedAt.IsZero() {
		t.Errorf("pruned entry changed: %+v", a)
	}
}
