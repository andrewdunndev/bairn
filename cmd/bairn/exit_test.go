package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/sync"
)

func TestFetchExitCode(t *testing.T) {
	cases := []struct {
		name string
		res  sync.Result
		want int
	}{
		{"clean", sync.Result{Saved: 3, Uploaded: 3}, 0},
		{"refused is not a failure", sync.Result{AlbumsRefused: 2}, 0},
		{"item error", sync.Result{Errors: 1}, 1},
		{"upload failed", sync.Result{UploadFailed: 1}, 1},
		{"exif error is warn-only", sync.Result{ExifErrors: 1}, 0},
		{"state error", sync.Result{StateErrors: 1}, 1},
		{"album error", sync.Result{AlbumErrors: 1}, 1},
		{"tag error", sync.Result{TagErrors: 1}, 1},
		{"tag refused is not a failure", sync.Result{TagsRefused: 2}, 0},
	}
	for _, c := range cases {
		if got := fetchExitCode(c.res, io.Discard); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
}

func TestRetryFailedRefusesFilterFlags(t *testing.T) {
	if err := checkRetryFailed(map[string]bool{"dry-run": true, "max-pages": true}); err != nil {
		t.Errorf("compatible flags refused: %v", err)
	}
	err := checkRetryFailed(map[string]bool{"source": true, "include-system-posts": true})
	if err == nil || !strings.Contains(err.Error(), "--include-system-posts, --source") {
		t.Errorf("err = %v, want both incompatible flags named", err)
	}
}

func TestRetryGoneNeedsRetryFailed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if got := runFetch(context.Background(), &config.Config{}, logger, []string{"--retry-gone"}); got != 2 {
		t.Errorf("exit %d, want 2", got)
	}
}
