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
