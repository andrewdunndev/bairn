package albums

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type fakeAPI struct {
	existing map[string]string
	created  []string
	shared   [][]string
	added    map[string][]string
	findErr  error
	finds    int
	addErr   error
}

func (f *fakeAPI) FindOwnedAlbum(_ context.Context, name string) (string, error) {
	f.finds++
	return f.existing[name], f.findErr
}
func (f *fakeAPI) CreateAlbum(_ context.Context, name string, users []string) (string, error) {
	f.created = append(f.created, name)
	f.shared = append(f.shared, users)
	id := "id-" + name
	f.existing[name] = id
	return id, nil
}
func (f *fakeAPI) AddAssetsToAlbum(_ context.Context, id string, assets []string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.added[id] = append(f.added[id], assets...)
	return nil
}

func newFake() *fakeAPI { return &fakeAPI{existing: map[string]string{}, added: map[string][]string{}} }

func TestDefaultNamesRender(t *testing.T) {
	zone := time.FixedZone("z", -5*3600)
	f := New(newFake(), Config{MonthPattern: DefaultMonthPattern, YearPattern: DefaultYearPattern, Zone: zone}, nil)
	got := f.Names(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC))
	if !slices.Equal(got, []string{"EDC November 2026", "EDC 2026"}) {
		t.Errorf("names = %v", got)
	}
}

func TestNamesUseRunZoneAtMonthBoundary(t *testing.T) {
	zone := time.FixedZone("z", -5*3600)
	f := New(newFake(), Config{MonthPattern: "%B %Y", YearPattern: "%Y", Zone: zone}, nil)
	// 02:00 UTC on 1 Dec is still 30 Nov, 21:00 in the zone.
	got := f.Names(time.Date(2026, 12, 1, 2, 0, 0, 0, time.UTC))
	if got[0] != "November 2026" {
		t.Errorf("month = %q, want November 2026", got[0])
	}
	// New Year in UTC is still last year in the zone.
	got = f.Names(time.Date(2027, 1, 1, 2, 0, 0, 0, time.UTC))
	if got[1] != "2026" {
		t.Errorf("year = %q, want 2026", got[1])
	}
}

func TestOnlyConfiguredPatternsAreUsed(t *testing.T) {
	f := New(newFake(), Config{YearPattern: "Y %Y"}, nil)
	if got := f.Names(time.Now()); len(got) != 1 {
		t.Errorf("names = %v, want year only", got)
	}
	if (Config{}).Enabled() {
		t.Error("empty config must be disabled")
	}
}

func TestCreatesMissingSharesAndBatches(t *testing.T) {
	api := newFake()
	f := New(api, Config{MonthPattern: "M %m", YearPattern: "Y %Y", ShareUserIDs: []string{"u1"}, Zone: time.UTC}, nil)
	ctx := context.Background()
	at := time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"a1", "a2", "a3"} {
		if err := f.Add(ctx, id, at); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.added) != 0 {
		t.Fatal("adds must be batched until Flush")
	}
	if err := f.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	slices.Sort(api.created)
	if !slices.Equal(api.created, []string{"M 05", "Y 2026"}) {
		t.Errorf("created = %v", api.created)
	}
	for _, s := range api.shared {
		if !slices.Equal(s, []string{"u1"}) {
			t.Errorf("shared = %v", s)
		}
	}
	if got := api.added["id-M 05"]; !slices.Equal(got, []string{"a1", "a2", "a3"}) {
		t.Errorf("month adds = %v", got)
	}
}

func TestExistingAlbumIsReusedAcrossRunsNeverRecreated(t *testing.T) {
	api := newFake()
	api.existing["Y 2026"] = "have"
	at := time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)
	for range 2 { // two runs
		f := New(api, Config{YearPattern: "Y %Y", Zone: time.UTC}, nil)
		_ = f.Add(context.Background(), "a1", at)
		if err := f.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.created) != 0 {
		t.Errorf("created = %v, want none", api.created)
	}
	if len(api.added["have"]) != 2 {
		t.Errorf("adds = %v", api.added)
	}
}

func TestLookupCachedWithinRun(t *testing.T) {
	api := newFake()
	f := New(api, Config{YearPattern: "Y %Y", Zone: time.UTC}, nil)
	at := time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)
	for i := range flushAt + 1 { // forces one early flush, then the final
		_ = f.Add(context.Background(), "a", at.Add(time.Duration(i)))
	}
	_ = f.Flush(context.Background())
	if len(api.created) != 1 {
		t.Errorf("created %v, want exactly one create across flushes", api.created)
	}
	if api.finds != 1 {
		t.Errorf("Find called %d times, want 1 across flushes", api.finds)
	}
}

func TestTakeFiledReportsOnlyFullyFiledAssets(t *testing.T) {
	api := newFake()
	f := New(api, Config{MonthPattern: "M %m", YearPattern: "Y %Y", Zone: time.UTC}, nil)
	at := time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)
	_ = f.Add(context.Background(), "a1", at)
	api.addErr = errors.New("down")
	if err := f.Flush(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if got := f.TakeFiled(); len(got) != 0 {
		t.Fatalf("filed after failed flush = %v, want none", got)
	}
	api.addErr = nil
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.TakeFiled(); len(got) != 1 || got[0] != "a1" {
		t.Fatalf("filed = %v, want [a1]", got)
	}
	if got := f.TakeFiled(); len(got) != 0 {
		t.Fatalf("filed drained twice = %v", got)
	}
}

func TestZeroPostDateIsRefused(t *testing.T) {
	f := New(newFake(), Config{YearPattern: "Y %Y", Zone: time.UTC}, nil)
	if err := f.Add(context.Background(), "a1", time.Time{}); err == nil {
		t.Fatal("want error for zero post date")
	}
	if len(f.pending) != 0 {
		t.Errorf("pending = %v, want none", f.pending)
	}
}

func TestFailedLookupKeepsAssetsQueued(t *testing.T) {
	api := newFake()
	api.findErr = errors.New("down")
	f := New(api, Config{YearPattern: "Y %Y", Zone: time.UTC}, nil)
	_ = f.Add(context.Background(), "a1", time.Now())
	if err := f.Flush(context.Background()); err == nil {
		t.Fatal("want error")
	}
	api.findErr = nil
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(api.added["id-Y "+time.Now().Format("2006")]); n != 1 {
		t.Errorf("adds after retry = %d, want 1", n)
	}
}
