package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirTightensExisting(t *testing.T) {
	base := t.TempDir()
	d := filepath.Join(base, "bairn")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", base)
	got, err := stateDir()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %o, want 700", fi.Mode().Perm())
	}
}

func TestLoadTightensDefaultSaveDir(t *testing.T) {
	base := t.TempDir()
	d := filepath.Join(base, "bairn", "assets")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", base)
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv("BAIRN_SAVE_DIR", "")
	t.Setenv("BAIRN_STATE_PATH", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(c.SaveDir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("mode = %o, want 700", fi.Mode().Perm())
	}
}

func TestValidateRejectsNonUUIDDeviceID(t *testing.T) {
	c := &Config{FamlyDeviceID: "bairn-default-device", FamlyEmail: "a@example.com", FamlyPassword: "x", SaveDir: "d"}
	if err := c.Validate("fetch"); err == nil {
		t.Error("want error for a non-UUID FAMLY_DEVICE_ID")
	}
	c.FamlyDeviceID = "0b6f3c1e-9d2a-4c1b-8f3e-1a2b3c4d5e6f"
	if err := c.Validate("fetch"); err != nil {
		t.Errorf("valid id: %v", err)
	}
}

func TestProgressEveryDefaultsAndOverrides(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("BAIRN_PROGRESS_EVERY", "")
	c, err := Load()
	if err != nil || c.ProgressEvery != DefaultProgressEvery {
		t.Fatalf("default: %v %v", c, err)
	}
	t.Setenv("BAIRN_PROGRESS_EVERY", "0")
	if c, err = Load(); err != nil || c.ProgressEvery != 0 {
		t.Errorf("0 should turn progress off: %v %v", c, err)
	}
	t.Setenv("BAIRN_PROGRESS_EVERY", "many")
	if _, err = Load(); err == nil {
		t.Error("a non-number must be refused")
	}
}

func TestTagDefaultsAndOverrides(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("BAIRN_TAG", "")
	t.Setenv("BAIRN_NO_TAG", "")
	c, err := Load()
	if err != nil || c.Tag != "bairn" || c.NoTag {
		t.Fatalf("default: %+v %v", c, err)
	}
	t.Setenv("BAIRN_TAG", "family")
	t.Setenv("BAIRN_NO_TAG", "1")
	if c, err = Load(); err != nil || c.Tag != "family" || !c.NoTag {
		t.Errorf("overrides: %+v %v", c, err)
	}
}
