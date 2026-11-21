package main

import (
	"io"
	"testing"

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
		{"album error", sync.Result{AlbumErrors: 1}, 1},
	}
	for _, c := range cases {
		if got := fetchExitCode(c.res, io.Discard); got != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, got, c.want)
		}
	}
}
