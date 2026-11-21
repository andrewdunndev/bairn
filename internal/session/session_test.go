package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripPermsAndAtomic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	p := filepath.Join(dir, FileName)
	s := Open(p, false)
	if got, err := s.Load(); err != nil || got != (Session{}) {
		t.Fatalf("missing file: %+v %v", got, err)
	}
	want := Session{DeviceID: "d", AccessToken: "t"}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || got != want {
		t.Fatalf("load = %+v %v", got, err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("leftover temp files: %d entries", len(ents))
	}
}

func TestCorruptFileIsEmptyWithErrorNotContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(`{"accessToken":"secret-tok"`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Open(p, false).Load()
	if err == nil || got != (Session{}) {
		t.Fatalf("got %+v err %v", got, err)
	}
	if strings.Contains(err.Error(), "secret-tok") {
		t.Error("error leaks file content")
	}
}

func TestReadOnlyStoreNeverWrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", FileName)
	if err := Open(p, true).Save(Session{AccessToken: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(p)); err == nil {
		t.Error("read-only save created the directory")
	}
}

// A planted symlink at the path is replaced by Save, never written
// through, and Load refuses to read through it.
func TestSymlinkAtPathIsReplacedAndNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, FileName)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	s := Open(p, false)
	if got, err := s.Load(); err == nil || got != (Session{}) {
		t.Fatalf("Load through symlink: %+v %v, want refusal", got, err)
	}
	want := Session{DeviceID: "d", AccessToken: "t"}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "untouched" {
		t.Errorf("symlink target was written: %q", b)
	}
	if fi, _ := os.Lstat(p); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("symlink was not replaced by a regular file")
	}
	if got, err := s.Load(); err != nil || got != want {
		t.Fatalf("reload = %+v %v", got, err)
	}
}
