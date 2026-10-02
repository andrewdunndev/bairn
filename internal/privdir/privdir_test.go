package privdir

import (
	"os"
	"path/filepath"
	"testing"
)

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestEnsureTightensExistingDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(d); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, d); got != 0o700 {
		t.Errorf("mode = %o, want 700", got)
	}
}

func TestEnsureCreatesAndLeavesParentAlone(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(parent, "a", "b")
	if err := Ensure(d); err != nil {
		t.Fatal(err)
	}
	if got := mode(t, d); got != 0o700 {
		t.Errorf("mode = %o, want 700", got)
	}
	if got := mode(t, parent); got != 0o755 {
		t.Errorf("parent mode = %o, want 755 untouched", got)
	}
}
