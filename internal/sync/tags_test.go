package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
	"gitlab.com/dunn.dev/bairn/internal/tags"
)

// tagImmich fakes the upload and tag endpoints. Uploads of the same
// bytes answer "duplicate" after the first. It records tag calls.
type tagImmich struct {
	upserts    atomic.Int32
	tagCalls   atomic.Int32
	tagged     []string // asset IDs in call order
	refuse     map[string]string
	forbidTags atomic.Bool
}

func newTagImmich(t *testing.T) (*httptest.Server, *tagImmich) {
	t.Helper()
	ti := &tagImmich{refuse: map[string]string{}}
	uploads := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/assets", func(w http.ResponseWriter, r *http.Request) {
		status := "created"
		if uploads++; uploads > 1 {
			status = "duplicate"
		}
		_, _ = fmt.Fprintf(w, `{"id":"asset-1","status":%q}`, status)
	})
	mux.HandleFunc("PUT /tags", func(w http.ResponseWriter, r *http.Request) {
		if ti.forbidTags.Load() {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		var in struct{ Tags []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		ti.upserts.Add(1)
		_, _ = fmt.Fprintf(w, `[{"id":"tag-1","name":%q,"value":%q}]`, in.Tags[0], in.Tags[0])
	})
	mux.HandleFunc("PUT /tags/{id}/assets", func(w http.ResponseWriter, r *http.Request) {
		if ti.forbidTags.Load() {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		var in struct{ IDs []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		ti.tagCalls.Add(1)
		var out []string
		for _, id := range in.IDs {
			if reason := ti.refuse[id]; reason != "" {
				out = append(out, fmt.Sprintf(`{"id":%q,"success":false,"error":%q}`, id, reason))
				continue
			}
			if slices.Contains(ti.tagged, id) {
				out = append(out, fmt.Sprintf(`{"id":%q,"success":false,"error":"duplicate"}`, id))
				continue
			}
			ti.tagged = append(ti.tagged, id)
			out = append(out, fmt.Sprintf(`{"id":%q,"success":true}`, id))
		}
		_, _ = w.Write([]byte("[" + strings.Join(out, ",") + "]"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, ti
}

func tagRun(t *testing.T, famlySrv *httptest.Server, ic *immich.Client, st *state.Store, withTag bool, opts Options) Result {
	t.Helper()
	disk, _ := sink.NewDisk(t.TempDir(), "", "")
	fc := famly.New(famly.NewStaticToken("t"), famly.WithBaseURL(famlySrv.URL), famly.WithPageDelay(0))
	deps := Deps{Famly: fc, Disk: disk, Immich: sink.NewImmich(ic), State: st}
	if withTag {
		deps.Tags = tags.New(ic, "bairn", nil)
	}
	opts.MaxPages, opts.Source = 2, SourceAll
	res, err := Run(context.Background(), deps, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestUploadsAreTaggedOnceAndARerunAddsNothing(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, ti := newTagImmich(t)
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)

	r1 := tagRun(t, famlySrv, ic, st, true, Options{})
	if r1.Uploaded != 1 || r1.TagErrors != 0 {
		t.Fatalf("run1 %+v", r1)
	}
	if ti.upserts.Load() != 1 || !slices.Equal(ti.tagged, []string{"asset-1"}) {
		t.Errorf("upserts=%d tagged=%v, want one tag creation and asset-1 tagged", ti.upserts.Load(), ti.tagged)
	}
	a, _ := st.Get(context.Background(), "img-1")
	if a.TaggedAt.IsZero() {
		t.Error("TaggedAt not recorded")
	}

	calls := ti.tagCalls.Load()
	r2 := tagRun(t, famlySrv, ic, st, true, Options{})
	if r2.Buckets.AlreadyDone != 1 || ti.upserts.Load() != 1 || ti.tagCalls.Load() != calls {
		t.Errorf("rerun made tag calls: upserts=%d calls=%d->%d", ti.upserts.Load(), calls, ti.tagCalls.Load())
	}
}

func TestDuplicateUploadIsTaggedToo(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, ti := newTagImmich(t)
	ic := immich.New(srv.URL, "k")
	// Burn the one "created" answer so the run's upload is a duplicate.
	_, _ = ic.Upload(context.Background(), immich.UploadInput{Data: []byte("x"), Filename: "x.jpg", FileCreatedAt: time.Now(), FileModifiedAt: time.Now()})
	st := openTestStore(t)
	r := tagRun(t, famlySrv, ic, st, true, Options{})
	if r.UploadDuplicates != 1 || !slices.Equal(ti.tagged, []string{"asset-1"}) {
		t.Errorf("dup=%d tagged=%v, want the duplicate tagged", r.UploadDuplicates, ti.tagged)
	}
}

func TestExistingUploadsAreBackFiledOnceFromState(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, ti := newTagImmich(t)
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)
	ctx := context.Background()
	for i := range 3 {
		id := fmt.Sprintf("old-%d", i)
		_ = st.Discover(ctx, id, state.Asset{Source: "feed-image"})
		_ = st.MarkSaved(ctx, id, "/x", time.Now())
		_ = st.MarkUploaded(ctx, id, fmt.Sprintf("old-asset-%d", i), "created", "", time.Now())
	}
	// Uploaded without an Immich ID cannot be tagged and is not queued.
	_ = st.Discover(ctx, "noid", state.Asset{Source: "feed-image"})
	_ = st.MarkUploaded(ctx, "noid", "", "created", "", time.Now())

	if n := len(st.Untagged()); n != 3 {
		t.Fatalf("Untagged = %d, want 3", n)
	}
	// A dry run must not tag.
	tagRun(t, famlySrv, ic, st, true, Options{DryRun: true})
	if len(ti.tagged) != 0 {
		t.Fatalf("dry run tagged %v", ti.tagged)
	}
	tagRun(t, famlySrv, ic, st, true, Options{})
	if len(ti.tagged) != 4 { // 3 back-filled plus the new upload
		t.Errorf("tagged = %v, want the 3 old assets and the new upload", ti.tagged)
	}
	if ti.upserts.Load() != 1 || ti.tagCalls.Load() != 1 {
		t.Errorf("upserts=%d calls=%d, want one tag creation and one batch for all four", ti.upserts.Load(), ti.tagCalls.Load())
	}
	if n := len(st.Untagged()); n != 0 {
		t.Errorf("Untagged after = %d, want 0", n)
	}
	calls := ti.tagCalls.Load()
	tagRun(t, famlySrv, ic, st, true, Options{})
	if ti.tagCalls.Load() != calls {
		t.Error("a second run re-queued tagged entries")
	}
}

func TestRefusedTagIsTerminalAndDoesNotFailTheRun(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, ti := newTagImmich(t)
	ti.refuse["asset-1"] = "no_permission"
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)
	r := tagRun(t, famlySrv, ic, st, true, Options{})
	if r.TagsRefused != 1 || r.TagErrors != 0 || r.Errors != 0 {
		t.Fatalf("refused=%d tagErrors=%d errors=%d", r.TagsRefused, r.TagErrors, r.Errors)
	}
	a, _ := st.Get(context.Background(), "img-1")
	if a.TagRefused != "no_permission" || !a.TaggedAt.IsZero() {
		t.Errorf("state = %+v", a)
	}
	calls := ti.tagCalls.Load()
	tagRun(t, famlySrv, ic, st, true, Options{})
	if ti.tagCalls.Load() != calls {
		t.Error("a refused asset was retried")
	}
}

func TestTagFailureIsCountedAndRetriedOnRerun(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, ti := newTagImmich(t)
	ti.forbidTags.Store(true)
	ic := immich.New(srv.URL, "k")
	st := openTestStore(t)
	r := tagRun(t, famlySrv, ic, st, true, Options{})
	if r.TagErrors == 0 || r.Uploaded != 1 || r.Errors != 0 {
		t.Fatalf("tagErrors=%d uploaded=%d errors=%d, want the upload kept and the tag failure counted", r.TagErrors, r.Uploaded, r.Errors)
	}
	if n := len(st.Untagged()); n != 1 {
		t.Fatalf("Untagged = %d, want the asset still owed", n)
	}
	ti.forbidTags.Store(false)
	r2 := tagRun(t, famlySrv, ic, st, true, Options{})
	if r2.TagErrors != 0 || !slices.Equal(ti.tagged, []string{"asset-1"}) {
		t.Errorf("rerun tagErrors=%d tagged=%v", r2.TagErrors, ti.tagged)
	}
}

func TestNoTagLeavesImmichTagsAlone(t *testing.T) {
	famlySrv := fakeFamlyServer(t)
	t.Cleanup(famlySrv.Close)
	srv, ti := newTagImmich(t)
	st := openTestStore(t)
	tagRun(t, famlySrv, immich.New(srv.URL, "k"), st, false, Options{})
	if ti.upserts.Load() != 0 || ti.tagCalls.Load() != 0 {
		t.Errorf("tag calls with tagging off: %d/%d", ti.upserts.Load(), ti.tagCalls.Load())
	}
}
