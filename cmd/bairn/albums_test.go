package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/config"
)

func TestStateGuardRefusesMissingStateWithMedia(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "2026-05-06"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-05-06", "x.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state.json")
	err := checkStateGuard(state, dir, false)
	if err == nil || !strings.Contains(err.Error(), "--allow-missing-state") {
		t.Fatalf("err = %v, want refusal naming --allow-missing-state", err)
	}
	if err := checkStateGuard(state, dir, true); err != nil {
		t.Errorf("override: %v", err)
	}
	if err := os.WriteFile(state, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkStateGuard(state, dir, false); err != nil {
		t.Errorf("state present: %v", err)
	}
}

func TestStateGuardIgnoresInFlightDownloads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".bairn-dl-123.part"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".bairn-dl-456.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkStateGuard(filepath.Join(dir, "state.json"), dir, false); err != nil {
		t.Errorf("in-flight temp counted as media: %v", err)
	}
}

func TestStateGuardAllowsEmptyOrMedialessSaveDir(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, ".bairn-state.json.lock")
	if err := os.WriteFile(state, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkStateGuard(filepath.Join(dir, "state.json"), dir, false); err != nil {
		t.Errorf("no media: %v", err)
	}
}

func TestAlbumConfig(t *testing.T) {
	const u = "0b6f3c1e-9d2a-4c1b-8f3e-1a2b3c4d5e6f"
	off, err := albumConfig(false, "", "", "", time.UTC)
	if err != nil || off.Enabled() {
		t.Errorf("default must be off: %+v %v", off, err)
	}
	on, err := albumConfig(true, "", "", u+", "+u, time.UTC)
	if err != nil || on.MonthPattern != "EDC %B %Y" || on.YearPattern != "EDC %Y" || len(on.ShareUserIDs) != 2 {
		t.Errorf("on: %+v %v", on, err)
	}
	one, _ := albumConfig(false, "", "Y %Y", "", time.UTC)
	if one.MonthPattern != "" || !one.Enabled() {
		t.Errorf("year only: %+v", one)
	}
	if _, err := albumConfig(true, "", "", "alice@example.com", time.UTC); err == nil {
		t.Error("non-UUID share must be refused")
	}
	if _, err := albumConfig(false, "", "", u, time.UTC); err == nil {
		t.Error("share without an album must be refused")
	}
}

func TestStateGuardRefusesUnreadableStatePath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := checkStateGuard(filepath.Join(blocker, "state.json"), dir, false)
	if err == nil || !strings.Contains(err.Error(), "cannot check state file") {
		t.Fatalf("err = %v, want refusal on non-ENOENT stat error", err)
	}
}

func fetchGuardCfg(t *testing.T, saveDir, statePath, famlyURL string) *config.Config {
	t.Helper()
	return &config.Config{
		FamlyAccessToken: "t", FamlyBaseURL: famlyURL,
		SaveDir: saveDir, StatePath: statePath,
	}
}

func TestFetchDryRunWithMissingStateAndMediaIsRefused(t *testing.T) {
	save := t.TempDir()
	if err := os.WriteFile(filepath.Join(save, "x.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	code := runFetch(context.Background(), fetchGuardCfg(t, save, statePath, srv.URL), slog.New(slog.DiscardHandler), []string{"--dry-run", "--save-dir", save})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if hit {
		t.Error("refusal came after contacting Famly")
	}
	if _, err := os.Stat(statePath); err == nil {
		t.Error("state file written by a refused run")
	}
}

func TestFetchDryRunNeverCreatesStateFile(t *testing.T) {
	save := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"feedItems":[]}`))
	}))
	defer srv.Close()
	if code := runFetch(context.Background(), fetchGuardCfg(t, save, statePath, srv.URL), slog.New(slog.DiscardHandler), []string{"--dry-run", "--save-dir", save}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if _, err := os.Stat(statePath); err == nil {
		t.Error("dry run created a state file")
	}
}
