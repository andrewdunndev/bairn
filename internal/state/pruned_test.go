package state

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var prunedAt = time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)

// seedPrunedStore holds one live saved+uploaded entry ("live"), one
// pruned ("pruned", file gone, everything else kept), and one verified
// but not pruned ("verified").
func seedPrunedStore(t *testing.T) *Store {
	t.Helper()
	s := tempStore(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for id, a := range map[string]Asset{
		"live":     {},
		"pruned":   {ImmichVerifiedAt: prunedAt, PrunedAt: prunedAt},
		"verified": {ImmichVerifiedAt: prunedAt},
	} {
		a.Source, a.FeedItemAt, a.DiscoveredAt = "feed-image", at, at
		a.DownloadedAt, a.SavedAt, a.SavedPath, a.SHA1 = at, at, "/disk/"+id+".jpg", "sha-"+id
		a.UploadedAt, a.ImmichAssetID, a.UploadSHA1 = at, "imm-"+id, "up-"+id
		if err := s.Discover(context.Background(), id, a); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestSavedFilesSkipsPruned(t *testing.T) {
	s := seedPrunedStore(t)
	var ids []string
	for _, f := range s.SavedFiles() {
		ids = append(ids, f.ID)
	}
	if got := strings.Join(ids, ","); got != "live,verified" {
		t.Errorf("SavedFiles = %s, want live,verified", got)
	}
}

func TestRequeueRefusesPruned(t *testing.T) {
	s := seedPrunedStore(t)
	if n := s.Requeue([]string{"pruned"}, "saved file missing"); n != 0 {
		t.Errorf("Requeue(pruned) = %d, want 0", n)
	}
	a, _ := s.Get(context.Background(), "pruned")
	if a.SavedAt.IsZero() || a.SavedPath == "" || a.LastError != "" || a.PrunedAt.IsZero() {
		t.Errorf("pruned entry changed: %+v", a)
	}
	if len(s.Failed()) != 0 {
		t.Errorf("Failed = %+v, want none", s.Failed())
	}
	if n := s.Requeue([]string{"live", "pruned"}, "x"); n != 1 {
		t.Errorf("Requeue(live, pruned) = %d, want 1", n)
	}
}

func TestPrunedEntryIsNotBackfillOrFailedWork(t *testing.T) {
	s := seedPrunedStore(t)
	if got := s.WithoutUploadSHA1(); len(got) != 0 {
		t.Errorf("WithoutUploadSHA1 = %+v, want none", got)
	}
	if ok, _ := s.IsSaved(context.Background(), "pruned"); !ok {
		t.Error("pruned entry not IsSaved: fetch would download it again")
	}
	if ok, _ := s.IsUploaded(context.Background(), "pruned"); !ok {
		t.Error("pruned entry not IsUploaded: fetch would upload it again")
	}
}

func TestStatsCountsVerifiedPrunedAndFailed(t *testing.T) {
	s := seedPrunedStore(t)
	_ = s.Discover(context.Background(), "bad", Asset{Source: "feed-image", UploadedAt: prunedAt, VerifyResult: "mismatch"})
	sm, _ := s.Stats(context.Background())
	if sm.Verified != 2 || sm.Pruned != 1 || sm.VerifyFailed != 1 {
		t.Errorf("verified=%d pruned=%d verifyFailed=%d, want 2 1 1", sm.Verified, sm.Pruned, sm.VerifyFailed)
	}
	b, _ := json.Marshal(sm)
	for _, k := range []string{`"verified":2`, `"pruned":1`, `"verifyFailed":1`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("summary JSON lacks %s: %s", k, b)
		}
	}
}

// A state file written by v0.8.1 has none of the new keys: it must
// load, stay unchanged through a flush, and write no new keys.
func TestV081StateLoadsWithoutNewFields(t *testing.T) {
	old := `{"img-1":{"source":"feed-image","feedItemId":"p1","discoveredAt":"2026-10-01T09:00:00Z","savedAt":"2026-10-01T09:00:00Z","savedPath":"/d/a.jpg","sha1":"s","uploadSha1":"u","uploadedAt":"2026-10-01T09:00:00Z","immichAssetId":"imm-1"}}`
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.Get(context.Background(), "img-1")
	if a.ImmichAssetID != "imm-1" || !a.PrunedAt.IsZero() || !a.ImmichVerifiedAt.IsZero() || a.VerifyResult != "" {
		t.Errorf("loaded %+v", a)
	}
	if len(s.SavedFiles()) != 1 {
		t.Error("old entry not listed by SavedFiles")
	}
	_ = s.Discover(context.Background(), "img-2", Asset{Source: "feed-image"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	for _, k := range []string{"immichVerifiedAt", "verifyResult", "prunedAt"} {
		if strings.Contains(string(b), k) {
			t.Errorf("unpruned state wrote %q: %s", k, b)
		}
	}
}

// The estate dedup script (immich_dedup.py load_bairn) reads these two
// keys straight from state.json: the map key (id) and immichAssetId.
func TestPrunedEntryKeepsDedupKeys(t *testing.T) {
	s := seedPrunedStore(t)
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	e, ok := raw["pruned"]
	if !ok {
		t.Fatal("pruned entry not keyed by its id")
	}
	if e["immichAssetId"] != "imm-pruned" || e["sha1"] != "sha-pruned" || e["savedPath"] != "/disk/pruned.jpg" {
		t.Errorf("pruned entry lost dedup fields: %v", e)
	}
	if _, ok := e["prunedAt"]; !ok {
		t.Error("prunedAt not persisted")
	}
}
