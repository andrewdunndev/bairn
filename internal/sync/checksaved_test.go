package sync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/asset"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

type fakeSaved struct {
	saved              bool
	isSavedErr, discEr error
	discovers          int
}

func (f *fakeSaved) IsSaved(context.Context, string) (bool, error) { return f.saved, f.isSavedErr }
func (f *fakeSaved) Discover(context.Context, string, state.Asset) error {
	f.discovers++
	return f.discEr
}

func TestCheckSavedCountsStateErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	disc := asset.DiscoverImage(famly.Image{ImageID: "img"}, famly.FeedItem{}, nil)
	boom := errors.New("boom")
	cases := []struct {
		name         string
		f            fakeSaved
		dry          bool
		already, ok  bool
		stateErrors  int
		wantDiscover int
	}{
		{"saved clean", fakeSaved{saved: true}, false, true, true, 0, 1},
		{"IsSaved error skips the item", fakeSaved{isSavedErr: boom}, false, false, false, 1, 0},
		{"IsSaved error with saved=true still skips", fakeSaved{saved: true, isSavedErr: boom}, false, false, false, 1, 0},
		{"stamp error counted, item continues", fakeSaved{saved: true, discEr: boom}, false, true, true, 1, 1},
		{"dry run does not stamp", fakeSaved{saved: true, discEr: boom}, true, true, true, 0, 0},
		{"unsaved is not stamped", fakeSaved{}, false, false, true, 0, 0},
	}
	for _, c := range cases {
		var res Result
		already, ok := checkSaved(context.Background(), &c.f, disc, c.dry, &res, log)
		if already != c.already || ok != c.ok || res.StateErrors != c.stateErrors || c.f.discovers != c.wantDiscover {
			t.Errorf("%s: already=%v ok=%v stateErrors=%d discovers=%d", c.name, already, ok, res.StateErrors, c.f.discovers)
		}
	}
}
