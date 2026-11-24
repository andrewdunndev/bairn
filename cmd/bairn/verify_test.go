package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/retry"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

var vHex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

type vKnown struct {
	id      string
	trashed bool
}

// immichFake enforces the v3.4.2 bulk-upload-check request shape and
// answers from a checksum table. failCall, when > 0, makes that call
// number answer 503.
type immichFake struct {
	table    map[string]vKnown
	calls    atomic.Int32
	failCall int32
	sizes    []int
	srv      *httptest.Server
}

func newImmichFake(t *testing.T, table map[string]vKnown) *immichFake {
	f := &immichFake{table: table}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		bad := func(msg string) { http.Error(w, `{"message":"`+msg+`"}`, http.StatusBadRequest) }
		if r.Method != http.MethodPost || r.URL.Path != "/assets/bulk-upload-check" ||
			r.Header.Get("x-api-key") != "k" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			bad("route or headers")
			return
		}
		var in struct {
			Assets []struct {
				ID       string `json:"id"`
				Checksum string `json:"checksum"`
			} `json:"assets"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || len(in.Assets) == 0 || len(in.Assets) > 500 {
			bad("body")
			return
		}
		f.sizes = append(f.sizes, len(in.Assets))
		if n == f.failCall {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		var out []map[string]any
		for _, a := range in.Assets {
			if a.ID == "" || !vHex40.MatchString(a.Checksum) {
				bad("item")
				return
			}
			if k, ok := f.table[a.Checksum]; ok {
				out = append(out, map[string]any{"id": a.ID, "action": "reject", "reason": "duplicate", "assetId": k.id, "isTrashed": k.trashed})
			} else {
				out = append(out, map[string]any{"id": a.ID, "action": "accept"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *immichFake) client() *immich.Client {
	return immich.New(f.srv.URL, "k", immich.WithRetry(retry.Policy{Attempts: 2, Initial: time.Millisecond, Max: time.Millisecond}))
}

func vsum(n int) string { return fmt.Sprintf("%040x", n) }

func vSeed(t *testing.T, dir string, entries map[string]state.Asset) *state.Store {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	for id, a := range entries {
		if a.Source == "" {
			a.Source = "feed-image"
		}
		a.FeedItemID, a.FeedItemAt, a.DiscoveredAt = "post-"+id, at, at
		if a.UploadedAt.IsZero() {
			a.UploadedAt = at
		}
		if err := st.Discover(context.Background(), id, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	return st
}

func vGet(t *testing.T, st *state.Store, id string) state.Asset {
	t.Helper()
	a, err := st.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestVerifyMapsEveryOutcome(t *testing.T) {
	f := newImmichFake(t, map[string]vKnown{
		vsum(1): {"imm-ok", false},
		vsum(2): {"imm-trashed", true},
		vsum(3): {"some-other-id", false},
		// vsum(4) unknown: absent
	})
	at := time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC)
	old := at.Add(-24 * time.Hour)
	st := vSeed(t, t.TempDir(), map[string]state.Asset{
		"ok":        {UploadSHA1: vsum(1), ImmichAssetID: "imm-ok"},
		"trashed":   {UploadSHA1: vsum(2), ImmichAssetID: "imm-trashed", VerifiedAt: old},
		"mismatch":  {UploadSHA1: vsum(3), ImmichAssetID: "imm-mine", VerifiedAt: old},
		"absent":    {UploadSHA1: vsum(4), ImmichAssetID: "imm-absent", VerifiedAt: old},
		"blank":     {ImmichAssetID: "imm-blank", VerifiedAt: old},
		"pruned":    {UploadSHA1: vsum(1), ImmichAssetID: "imm-ok", PrunedAt: old},
		"unflagged": {UploadSHA1: vsum(1), ImmichAssetID: "imm-ok", VerifyResult: "trashed"},
	})
	sum := verifyEntries(context.Background(), st, sink.NewImmich(f.client()), false, at, quiet)
	want := verifySummary{Verified: 2, Trashed: 1, Mismatch: 1, AbsentInImmich: 1, Unverifiable: 1}
	if sum != want {
		t.Fatalf("summary %+v, want %+v", sum, want)
	}
	for id, w := range map[string]struct {
		verified bool
		result   string
	}{
		"ok": {true, ""}, "unflagged": {true, ""},
		"trashed": {false, "trashed"}, "mismatch": {false, "mismatch"}, "absent": {false, "absent"},
		"blank": {false, "unverifiable"}, "pruned": {false, ""},
	} {
		a := vGet(t, st, id)
		if !a.VerifiedAt.IsZero() != w.verified || a.VerifyResult != w.result {
			t.Errorf("%s: verifiedAt=%v result=%q, want verified=%v result=%q", id, a.VerifiedAt, a.VerifyResult, w.verified, w.result)
		}
	}
	if a := vGet(t, st, "ok"); !a.VerifiedAt.Equal(at) {
		t.Errorf("verifiedAt = %v, want %v", a.VerifiedAt, at)
	}
	if a := vGet(t, st, "pruned"); !a.VerifiedAt.IsZero() || a.VerifyResult != "" {
		t.Error("pruned entry touched")
	}
	if len(f.sizes) != 1 || f.sizes[0] != 5 {
		t.Errorf("request sizes %v, want one request of the 5 checkable entries", f.sizes)
	}
}

func TestVerifyDemotesALaterNonVerifiedResult(t *testing.T) {
	f := newImmichFake(t, map[string]vKnown{vsum(1): {"imm-1", true}})
	st := vSeed(t, t.TempDir(), map[string]state.Asset{
		"a": {UploadSHA1: vsum(1), ImmichAssetID: "imm-1", VerifiedAt: time.Now()},
	})
	verifyEntries(context.Background(), st, sink.NewImmich(f.client()), false, time.Now(), quiet)
	if a := vGet(t, st, "a"); !a.VerifiedAt.IsZero() || a.VerifyResult != "trashed" {
		t.Errorf("%+v: want the verified stamp cleared and result trashed", a)
	}
}

func TestVerifyDryRunWritesNothing(t *testing.T) {
	f := newImmichFake(t, map[string]vKnown{vsum(1): {"imm-1", false}})
	dir := t.TempDir()
	st := vSeed(t, dir, map[string]state.Asset{"a": {UploadSHA1: vsum(1), ImmichAssetID: "imm-1"}})
	path := filepath.Join(dir, "state.json")
	before, _ := os.ReadFile(path)
	sum := verifyEntries(context.Background(), st, sink.NewImmich(f.client()), true, time.Now(), quiet)
	if sum.Verified != 1 {
		t.Fatalf("dry run should still count: %+v", sum)
	}
	if a := vGet(t, st, "a"); !a.VerifiedAt.IsZero() {
		t.Error("dry run stamped memory")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("dry run changed the state file")
	}
}

func TestVerifyBatchFailureIsolation(t *testing.T) {
	f := newImmichFake(t, nil)
	entries := map[string]state.Asset{}
	for i := 0; i < 501; i++ {
		entries[fmt.Sprintf("e%04d", i)] = state.Asset{UploadSHA1: vsum(i + 10), ImmichAssetID: "x"}
	}
	st := vSeed(t, t.TempDir(), entries)
	f.table = map[string]vKnown{vsum(510): {"x", false}}
	// Batch 1 fails on both attempts (calls 1 and 2); batch 2 is call 3.
	f.failCall = 0
	var n atomic.Int32
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := n.Add(1); c <= 2 {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		r.Header.Set("x-api-key", "k")
		f.srv.Config.Handler.ServeHTTP(w, r)
	})
	front := httptest.NewServer(failing)
	defer front.Close()
	ic := immich.New(front.URL, "SECRET-KEY", immich.WithRetry(retry.Policy{Attempts: 2, Initial: time.Millisecond, Max: time.Millisecond}))
	var logs strings.Builder
	sum := verifyEntries(context.Background(), st, sink.NewImmich(ic), false, time.Now(), slog.New(slog.NewTextHandler(&logs, nil)))
	if logs.Len() == 0 || strings.Contains(logs.String(), "SECRET-KEY") {
		t.Errorf("want a log line without the key, got %q", logs.String())
	}
	if sum.Errors != 500 || sum.Verified != 1 || sum.AbsentInImmich != 0 {
		t.Fatalf("summary %+v, want errors=500 verified=1", sum)
	}
	if a := vGet(t, st, "e0000"); !a.VerifiedAt.IsZero() || a.VerifyResult != "" {
		t.Errorf("failed batch was stamped: %+v", a)
	}
	if a := vGet(t, st, "e0500"); a.VerifiedAt.IsZero() {
		t.Error("the good batch was not stamped")
	}
	if len(f.sizes) != 1 || f.sizes[0] != 1 {
		t.Errorf("sizes %v: only batch 2 should have reached the fake", f.sizes)
	}
}

func TestVerifyUnauthorizedAbortsAndStampsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer srv.Close()
	entries := map[string]state.Asset{}
	for i := 0; i < 501; i++ {
		entries[fmt.Sprintf("e%04d", i)] = state.Asset{UploadSHA1: vsum(i + 10), ImmichAssetID: "x"}
	}
	st := vSeed(t, t.TempDir(), entries)
	sum := verifyEntries(context.Background(), st, sink.NewImmich(immich.New(srv.URL, "k")), false, time.Now(), quiet)
	if sum.Errors != 501 || sum.Verified+sum.AbsentInImmich != 0 {
		t.Fatalf("summary %+v, want all 501 errors and no stamps", sum)
	}
	if a := vGet(t, st, "e0000"); a.VerifyResult != "" {
		t.Error("stamped on auth failure")
	}
}

func verifyCfg(t *testing.T, url string) *config.Config {
	return &config.Config{StatePath: filepath.Join(t.TempDir(), "state.json"), ImmichBaseURL: url, ImmichAPIKey: "SECRET-KEY"}
}

func TestRunVerifyExitCodesAndSummary(t *testing.T) {
	f := newImmichFake(t, map[string]vKnown{vsum(1): {"imm-1", false}})
	f.srv.Config.Handler = wrapKey(f.srv.Config.Handler)
	cfg := verifyCfg(t, f.srv.URL)
	st, _ := state.Open(context.Background(), cfg.StatePath)
	at := time.Now()
	_ = st.Discover(context.Background(), "a", state.Asset{Source: "feed-image", UploadedAt: at, UploadSHA1: vsum(1), ImmichAssetID: "imm-1"})
	_ = st.Close()
	if code := runVerify(context.Background(), cfg, quiet, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	st, _ = state.Open(context.Background(), cfg.StatePath)
	defer st.Close()
	if a := vGet(t, st, "a"); a.VerifiedAt.IsZero() {
		t.Error("not stamped on disk")
	}
}

// wrapKey lets the fake, which expects key "k", accept the test config's key.
func wrapKey(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("x-api-key", "k")
		h.ServeHTTP(w, r)
	})
}

func TestRunVerifyUsageAndAuthFailureExit(t *testing.T) {
	if code := runVerify(context.Background(), verifyCfg(t, ""), quiet, nil); code != 2 {
		t.Errorf("no immich config: exit %d, want 2", code)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer srv.Close()
	cfg := verifyCfg(t, srv.URL)
	st, _ := state.Open(context.Background(), cfg.StatePath)
	_ = st.Discover(context.Background(), "a", state.Asset{Source: "feed-image", UploadedAt: time.Now(), UploadSHA1: vsum(1), ImmichAssetID: "i"})
	_ = st.Close()
	if code := runVerify(context.Background(), cfg, quiet, nil); code != 1 {
		t.Errorf("401: exit %d, want 1", code)
	}
}

func TestRunVerifyHoldsTheStateLock(t *testing.T) {
	cfg := verifyCfg(t, "http://127.0.0.1:1")
	st, err := state.Open(context.Background(), cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if code := runVerify(context.Background(), cfg, quiet, nil); code != 1 {
		t.Errorf("locked state: exit %d, want 1", code)
	}
}
