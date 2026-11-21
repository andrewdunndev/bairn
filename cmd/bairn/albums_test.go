package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
