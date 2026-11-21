package asset

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepStaleTempsRemovesOnlyOldPartFiles(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "2026", "05")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	mk := func(p string, mod time.Time) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, mod, mod)
	}
	stale := filepath.Join(sub, TempPrefix+"1.part")
	fresh := filepath.Join(sub, TempPrefix+"2.part")
	media := filepath.Join(sub, "photo.jpg")
	oldMedia := filepath.Join(sub, TempPrefix+"3.jpg") // not a .part: left alone
	mk(stale, old)
	mk(fresh, time.Now())
	mk(media, old)
	mk(oldMedia, old)
	if n := SweepStaleTemps(root, 24*time.Hour); n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	for p, want := range map[string]bool{stale: false, fresh: true, media: true, oldMedia: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}
