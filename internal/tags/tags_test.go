package tags

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

type fakeAPI struct {
	upserts   int
	upsertErr error
	batches   [][]string
	refuse    map[string]string
	failTag   int // fail this many TagAssets calls first
}

func (f *fakeAPI) UpsertTag(_ context.Context, name string) (string, error) {
	f.upserts++
	return "tag-" + name, f.upsertErr
}

func (f *fakeAPI) TagAssets(_ context.Context, id string, ids []string) (map[string]string, error) {
	if f.failTag > 0 {
		f.failTag--
		return nil, errors.New("boom")
	}
	if id != "tag-bairn" {
		return nil, fmt.Errorf("wrong tag %s", id)
	}
	f.batches = append(f.batches, slices.Clone(ids))
	return f.refuse, nil
}

func TestTagIsResolvedOnceAndAssetsBatch(t *testing.T) {
	api := &fakeAPI{}
	tg := New(api, "bairn", nil)
	ctx := context.Background()
	for i := range 250 {
		if err := tg.Add(ctx, fmt.Sprintf("a-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tg.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if api.upserts != 1 {
		t.Errorf("upserts = %d, want 1 per run", api.upserts)
	}
	if len(api.batches) != 3 || len(api.batches[0]) != 100 || len(api.batches[2]) != 50 {
		t.Errorf("batches = %d, want 100+100+50", len(api.batches))
	}
	if got := tg.TakeTagged(); len(got) != 250 {
		t.Errorf("tagged = %d, want 250", len(got))
	}
	if len(tg.TakeTagged()) != 0 {
		t.Error("TakeTagged must forget")
	}
}

func TestFlushWithNothingQueuedMakesNoCall(t *testing.T) {
	api := &fakeAPI{}
	if err := New(api, "bairn", nil).Flush(context.Background()); err != nil || api.upserts != 0 {
		t.Errorf("err=%v upserts=%d, want none", err, api.upserts)
	}
}

func TestRefusedAssetIsFinalAndNotTagged(t *testing.T) {
	api := &fakeAPI{refuse: map[string]string{"gone": "no_permission"}}
	tg := New(api, "bairn", nil)
	ctx := context.Background()
	_ = tg.Add(ctx, "ok")
	_ = tg.Add(ctx, "gone")
	if err := tg.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := tg.TakeTagged(); !slices.Equal(got, []string{"ok"}) {
		t.Errorf("tagged = %v, want [ok]", got)
	}
	if r := tg.TakeRefused(); r["gone"] != "no_permission" || len(r) != 1 {
		t.Errorf("refused = %v", r)
	}
}

func TestFailedFlushKeepsAssetsQueued(t *testing.T) {
	api := &fakeAPI{failTag: 1}
	tg := New(api, "bairn", nil)
	ctx := context.Background()
	_ = tg.Add(ctx, "a")
	if err := tg.Flush(ctx); err == nil {
		t.Fatal("want the failure surfaced")
	}
	if len(tg.TakeTagged()) != 0 {
		t.Error("nothing was tagged yet")
	}
	if err := tg.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := tg.TakeTagged(); !slices.Equal(got, []string{"a"}) {
		t.Errorf("tagged after retry = %v", got)
	}
	if api.upserts != 1 {
		t.Errorf("upserts = %d: the tag is not re-resolved after a failed batch", api.upserts)
	}
}

func TestUpsertFailureIsReported(t *testing.T) {
	api := &fakeAPI{upsertErr: errors.New("no key scope")}
	tg := New(api, "bairn", nil)
	_ = tg.Add(context.Background(), "a")
	if err := tg.Flush(context.Background()); err == nil {
		t.Fatal("want upsert failure")
	}
}

func TestValidateName(t *testing.T) {
	for name, ok := range map[string]bool{"bairn": true, "family photos": true, "": false, "  ": false, "a/b": false} {
		if err := ValidateName(name); (err == nil) != ok {
			t.Errorf("ValidateName(%q) = %v, want ok=%v", name, err, ok)
		}
	}
}

func TestFailedFlushStopsEarlyFlushesUntilTheFinalOne(t *testing.T) {
	api := &fakeAPI{failTag: 1}
	tg := New(api, "bairn", nil)
	ctx := context.Background()
	var firstErr error
	for i := range 350 {
		if err := tg.Add(ctx, fmt.Sprintf("a-%d", i)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		t.Fatal("the first full batch should have failed")
	}
	if len(api.batches) != 0 {
		t.Errorf("batches sent after the failure: %d", len(api.batches))
	}
	if err := tg.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := tg.TakeTagged(); len(got) != 350 {
		t.Errorf("final flush tagged %d, want all 350 still queued", len(got))
	}
}
