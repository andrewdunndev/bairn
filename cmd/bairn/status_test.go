package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// seedStatus writes a state with: ok (dated, file present), undated
// (no feedItemAt, file present), gone (file absent), blank (SavedAt set,
// no path), dir (path is a directory), and fresh (discovered only).
func seedStatus(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	ctx := context.Background()
	s, err := state.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	file := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	add := func(id, p string, feedAt time.Time) {
		_ = s.Discover(ctx, id, state.Asset{Source: "feed_image", FeedItemAt: feedAt})
		_ = s.MarkSaved(ctx, id, p, at)
	}
	add("ok", file("ok.jpg"), at)
	add("undated", file("undated.jpg"), time.Time{})
	add("gone", filepath.Join(dir, "gone.jpg"), at)
	add("blank", "", at)
	add("dir", dir, at)
	_ = s.Discover(ctx, "fresh", state.Asset{Source: "feed_image"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func runStatusCapture(t *testing.T, statePath string, args ...string) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := runStatus(context.Background(), &config.Config{StatePath: statePath}, slog.New(slog.NewTextHandler(io.Discard, nil)), args)
	os.Stdout = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	return string(b), code
}

// TestStatusLeavesStateUntouched pins that status is read-only, on both paths.
func TestStatusLeavesStateUntouched(t *testing.T) {
	for _, args := range [][]string{nil, {"--missing"}} {
		path := seedStatus(t)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, code := runStatusCapture(t, path, args...); code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("%v: status changed state.json", args)
		}
	}
}

func TestStatusCountsUndatedAndMissingFile(t *testing.T) {
	out, code := runStatusCapture(t, seedStatus(t))
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got struct{ Undated, MissingFile int }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	// undated: the one saved entry with no feedItemAt (fresh is unsaved).
	// missingFile: gone, blank, dir.
	if got.Undated != 1 || got.MissingFile != 3 {
		t.Errorf("undated=%d missingFile=%d, want 1 and 3", got.Undated, got.MissingFile)
	}
}

func TestStatusMissingPrintsOnlyIDs(t *testing.T) {
	out, code := runStatusCapture(t, seedStatus(t), "--missing")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if want := []string{"blank", "dir", "gone"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("--missing printed %q, want %q", lines, want)
	}
}

// TestStatSavedClassifies pins the empty/absent vs other-failure split backfill relies on.
func TestStatSavedClassifies(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	_ = os.WriteFile(f, nil, 0o600)
	for _, c := range []struct {
		path    string
		missing bool
		err     bool
	}{{"", true, true}, {filepath.Join(dir, "no"), true, true}, {dir, false, true}, {f, false, false}} {
		_, err := statSaved(c.path)
		if (err != nil) != c.err || (err == errNoSavedFile) != c.missing {
			t.Errorf("%q: err=%v", c.path, err)
		}
	}
}
