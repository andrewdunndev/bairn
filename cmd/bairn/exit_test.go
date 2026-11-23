package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/dunn.dev/bairn/api/famly"
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

type errCloser struct{ err error }

func (c errCloser) Close() error { return c.err }

func TestCloseStateSetsExit(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct {
		name  string
		err   error
		start int
		want  int
	}{
		{"clean close keeps 0", nil, 0, 0},
		{"failed close turns 0 into 1", errors.New("flush"), 0, 1},
		{"failed close keeps an existing code", errors.New("flush"), 2, 2},
	} {
		code := c.start
		closeState(errCloser{c.err}, log, &code)
		if code != c.want {
			t.Errorf("%s: code %d, want %d", c.name, code, c.want)
		}
	}
}

func TestHouseholdSetsFailsOnRelationsError(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, relOK := range []bool{true, false} {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/me/me/me", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"loginId":"me","roles2":[{"targetId":"c1"},{"targetId":"c2"}]}`)
		})
		mux.HandleFunc("/api/v2/relations", func(w http.ResponseWriter, r *http.Request) {
			if !relOK && r.URL.Query().Get("childId") == "c2" {
				http.Error(w, "boom", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `[{"loginId":"other"}]`)
		})
		srv := httptest.NewServer(mux)
		fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(srv.URL), famly.WithPageDelay(0))
		logins, children, err := householdSets(context.Background(), fc, log)
		srv.Close()
		if relOK {
			if err != nil || len(logins) != 2 || len(children) != 2 {
				t.Errorf("ok: err=%v logins=%v children=%v", err, logins, children)
			}
		} else if err == nil {
			t.Errorf("relations failure: want error, got logins=%v", logins)
		}
	}
}
