package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/asset"
	"gitlab.com/dunn.dev/bairn/internal/sink"
)

type fakeSaved struct {
	saved      bool
	isSavedErr error
	stamps     bool // what StampFeedItemAt reports
	stampCalls int
}

func (f *fakeSaved) IsSaved(context.Context, string) (bool, error) { return f.saved, f.isSavedErr }
func (f *fakeSaved) StampFeedItemAt(string, time.Time) bool {
	f.stampCalls++
	return f.stamps
}

func TestCheckSavedCountsStamps(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	disc := asset.DiscoverImage(famly.Image{ImageID: "img"}, famly.FeedItem{}, nil)
	boom := errors.New("boom")
	cases := []struct {
		name        string
		f           fakeSaved
		dry         bool
		already, ok bool
		stateErrors int
		stamped     int
		wantCalls   int
	}{
		{"saved, stamp written", fakeSaved{saved: true, stamps: true}, false, true, true, 0, 1, 1},
		{"saved, already dated", fakeSaved{saved: true}, false, true, true, 0, 0, 1},
		{"IsSaved error skips the item", fakeSaved{isSavedErr: boom, stamps: true}, false, false, false, 1, 0, 0},
		{"IsSaved error with saved=true still skips", fakeSaved{saved: true, isSavedErr: boom, stamps: true}, false, false, false, 1, 0, 0},
		{"dry run does not stamp", fakeSaved{saved: true, stamps: true}, true, true, true, 0, 0, 0},
		{"unsaved is not stamped", fakeSaved{stamps: true}, false, false, true, 0, 0, 0},
	}
	for _, c := range cases {
		var res Result
		already, ok := checkSaved(context.Background(), &c.f, disc, c.dry, &res, log)
		if already != c.already || ok != c.ok || res.StateErrors != c.stateErrors || res.Stamped != c.stamped || c.f.stampCalls != c.wantCalls {
			t.Errorf("%s: already=%v ok=%v stateErrors=%d stamped=%d calls=%d", c.name, already, ok, res.StateErrors, res.Stamped, c.f.stampCalls)
		}
	}
}

type failingSaved struct{ fakeSaved }

// TestProcessOneSkipsItemOnIsSavedError pins the wiring: an IsSaved
// failure must fail the item, not fall through to a download.
func TestProcessOneSkipsItemOnIsSavedError(t *testing.T) {
	var downloads int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/api/feed/feed/feed", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			_, _ = w.Write([]byte(`{"feedItems": []}`))
			return
		}
		fmt.Fprintf(w, `{"feedItems":[{"feedItemId":"post-1","createdDate":"2026-05-06T14:00:00Z","body":"x","videos":[],
			"images":[{"imageId":"img-1","url":"%[1]s/img/1","url_big":"%[1]s/img/1/big","width":8,"height":6,
			"createdAt":{"date":"2026-05-06T14:00:00Z"},"expiration":"2026-05-07T14:00:00Z","liked":false,"likes":[],"tags":[]}]}]}`, srv.URL)
	})
	mux.HandleFunc("/img/1/big", func(w http.ResponseWriter, r *http.Request) {
		downloads++
		_, _ = w.Write([]byte("x"))
	})
	saveDir := t.TempDir()
	disk, err := sink.NewDisk(saveDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	st := openTestStore(t)
	deps := Deps{
		Famly: famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(srv.URL), famly.WithPageDelay(0)),
		Disk:  disk, State: st,
		saved: &failingSaved{fakeSaved{isSavedErr: errors.New("boom")}},
	}
	res, err := Run(context.Background(), deps, Options{MaxPages: 2, Source: SourceAll})
	if err != nil {
		t.Fatal(err)
	}
	if res.Buckets.Failed != 1 || res.StateErrors != 1 || downloads != 0 || res.Saved != 0 {
		t.Errorf("Failed=%d StateErrors=%d downloads=%d Saved=%d; want 1, 1, 0, 0",
			res.Buckets.Failed, res.StateErrors, downloads, res.Saved)
	}
	if ents, _ := os.ReadDir(saveDir); len(ents) != 0 {
		t.Errorf("save dir has %d entries, want 0", len(ents))
	}
}
