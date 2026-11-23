package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

func shaOf(b string) string {
	h := sha1.Sum([]byte(b))
	return hex.EncodeToString(h[:])
}

var pruneAt = time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)

// pEnv is a save root, a state file outside it and a fake Immich that
// knows every entry added with add.
type pEnv struct {
	t     *testing.T
	root  string
	state string
	table map[string]vKnown
	ents  map[string]state.Asset
}

func newPEnv(t *testing.T) *pEnv {
	t.Helper()
	return &pEnv{t: t, root: filepath.Join(t.TempDir(), "assets"), state: filepath.Join(t.TempDir(), "state.json"),
		table: map[string]vKnown{}, ents: map[string]state.Asset{}}
}

func (e *pEnv) write(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// add records a verified, uploaded entry whose file holds content and
// whose upload hash is of uploaded, with a sidecar. Immich knows it.
func (e *pEnv) add(id, content, uploaded string) string {
	p := e.write("2026/"+id+".jpg", content)
	x := e.write("2026/"+id+".jpg.xmp", "xmp-"+id)
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(x, old, old); err != nil {
		e.t.Fatal(err)
	}
	e.entry(id, p, uploaded, pruneAt.Add(-time.Hour), "")
	e.table[shaOf(uploaded)] = vKnown{id: "imm-" + id}
	return p
}

func (e *pEnv) entry(id, path, uploaded string, verified time.Time, result string) {
	e.ents[id] = state.Asset{Source: "feed-video", SavedAt: pruneAt, SavedPath: path, SHA1: "dl-" + id, UploadSHA1: shaOf(uploaded),
		ImmichAssetID: "imm-" + id, ImmichVerifiedAt: verified, VerifyResult: result}
}

// seed writes the entries to the state file and closes it.
func (e *pEnv) seed() {
	e.t.Helper()
	st := vSeed(e.t, filepath.Dir(e.state), e.ents)
	if err := st.Close(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *pEnv) cfg(url string) *config.Config {
	return &config.Config{StatePath: e.state, SaveDir: e.root, ImmichBaseURL: url, ImmichAPIKey: "SECRET-KEY"}
}

func (e *pEnv) get(id string) state.Asset {
	e.t.Helper()
	st, err := state.Open(context.Background(), e.state)
	if err != nil {
		e.t.Fatal(err)
	}
	defer st.Close()
	return vGet(e.t, st, id)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// snapshot is every file under dirs: path, mode, content. Lock files
// are skipped: opening the state creates one, as verify and repair do.
func snapshot(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil || strings.HasSuffix(p, ".lock") {
				return nil
			}
			fi, _ := os.Lstat(p)
			s := fi.Mode().String()
			if fi.Mode().IsRegular() {
				b, _ := os.ReadFile(p)
				s += string(b)
			}
			out[p] = s
			return nil
		})
	}
	return out
}

func runPruneCapture(t *testing.T, cfg *config.Config, args ...string) (pruneSummary, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := runPrune(context.Background(), cfg, quiet, args)
	os.Stdout = old
	_ = w.Close()
	b, _ := io.ReadAll(r)
	var sum pruneSummary
	if len(b) > 0 {
		if err := json.Unmarshal(b, &sum); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
	}
	return sum, code
}

func (e *pEnv) fake() *immichFake {
	f := newImmichFake(e.t, e.table)
	f.srv.Config.Handler = wrapKey(f.srv.Config.Handler)
	return f
}

// decisionEnv has one entry per decision.
func decisionEnv(t *testing.T) *pEnv {
	e := newPEnv(t)
	e.add("ok", "bytes-ok", "bytes-ok")
	e.add("ok2", "bytes-ok2", "bytes-ok2")
	e.add("ok3", "bytes-ok3", "bytes-ok3")
	// re-saved after upload: the file now holds bytes Immich lacks.
	e.add("resaved", "NEW-EXIF-BYTES", "bytes-resaved")
	// never verified, and stamped failed.
	e.add("unver", "bytes-unver", "bytes-unver")
	e.entry("unver", e.ents["unver"].SavedPath, "bytes-unver", time.Time{}, "")
	e.add("stale", "bytes-stale", "bytes-stale")
	e.entry("stale", e.ents["stale"].SavedPath, "bytes-stale", pruneAt, "trashed")
	// verified earlier, trashed in Immich since.
	e.add("trashed", "bytes-trashed", "bytes-trashed")
	e.table[shaOf("bytes-trashed")] = vKnown{id: "imm-trashed", trashed: true}
	// verified earlier, Immich now holds the bytes under another id.
	e.add("moved", "bytes-moved", "bytes-moved")
	e.table[shaOf("bytes-moved")] = vKnown{id: "someone-else"}
	// verified, file gone.
	e.add("gone", "x", "bytes-gone")
	_ = os.Remove(e.ents["gone"].SavedPath)
	e.seed()
	return e
}

func TestPruneApplyDecidesEachEntry(t *testing.T) {
	e := decisionEnv(t)
	f := e.fake()
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := pruneSummary{Considered: 9, Pruned: 3, NotVerified: 2, ReverifyFailed: 2, LocalDiffers: 1, MissingFile: 1,
		BytesFreed: int64(3 * (len("bytes-ok") + len("xmp-ok")))}
	want.BytesFreed = int64(len("bytes-ok")+len("bytes-ok2")+len("bytes-ok3")) + int64(len("xmp-ok")+len("xmp-ok2")+len("xmp-ok3"))
	if sum != want {
		t.Fatalf("summary\n got %+v\nwant %+v", sum, want)
	}
	for _, id := range []string{"ok", "ok2", "ok3"} {
		p := e.ents[id].SavedPath
		if exists(p) || exists(p+".xmp") {
			t.Errorf("%s: media or sidecar left", id)
		}
		if a := e.get(id); a.PrunedAt.IsZero() || a.SavedPath != p || a.SavedAt.IsZero() || a.ImmichAssetID == "" {
			t.Errorf("%s: bad tombstone %+v", id, a)
		}
	}
	for _, id := range []string{"resaved", "unver", "stale", "trashed", "moved", "gone"} {
		if !e.get(id).PrunedAt.IsZero() {
			t.Errorf("%s pruned", id)
		}
		if id != "gone" && (!exists(e.ents[id].SavedPath) || !exists(e.ents[id].SavedPath+".xmp")) {
			t.Errorf("%s: file deleted", id)
		}
	}
	// A failed re-verify demotes the stamp.
	if a := e.get("trashed"); !a.ImmichVerifiedAt.IsZero() || a.VerifyResult != "trashed" {
		t.Errorf("trashed not demoted: %+v", a)
	}
	if a := e.get("moved"); !a.ImmichVerifiedAt.IsZero() || a.VerifyResult != "mismatch" {
		t.Errorf("moved not demoted: %+v", a)
	}
	// Entries never verified made no Immich call: 7 sent.
	if n := f.sizes; len(n) != 1 || n[0] != 7 {
		t.Errorf("bulk check sizes = %v, want [7]", n)
	}
}

func TestPruneDryRunChangesNothingByteForByte(t *testing.T) {
	e := decisionEnv(t)
	f := e.fake()
	before := snapshot(t, e.root, filepath.Dir(e.state))
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL))
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if after := snapshot(t, e.root, filepath.Dir(e.state)); !mapsEqual(before, after) {
		t.Fatalf("dry run changed the tree:\n%v\n%v", keys(before), keys(after))
	}
	if !sum.DryRun || sum.Pruned != 0 || sum.WouldPrune != 3 || sum.BytesFreed != 0 || sum.WouldFreeBytes == 0 ||
		sum.ReverifyFailed != 2 || sum.LocalDiffers != 1 {
		t.Errorf("dry summary %+v", sum)
	}
	if f.calls.Load() == 0 {
		t.Error("dry run made no re-verify call")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	var k []string
	for p := range m {
		k = append(k, p)
	}
	sort.Strings(k)
	return k
}

func TestPruneRefusesUnsafePaths(t *testing.T) {
	e := newPEnv(t)
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	_ = os.WriteFile(outside, []byte("bytes-out"), 0o600)
	e.add("pad1", "p1", "p1")
	e.add("pad2", "p2", "p2")
	e.add("pad3", "p3", "p3")
	// outside the root, plain and by ..
	e.add("out", "bytes-out", "bytes-out")
	e.entry("out", outside, "bytes-out", pruneAt.Add(-time.Hour), "")
	outside2 := filepath.Join(filepath.Dir(outside), "outside2.jpg")
	_ = os.WriteFile(outside2, []byte("bytes-dots"), 0o600)
	e.add("dots", "bytes-dots", "bytes-dots")
	e.entry("dots", filepath.Join(e.root, "2026", "..", "..", filepath.Base(filepath.Dir(outside)), "outside2.jpg"), "bytes-dots", pruneAt.Add(-time.Hour), "")
	// a symlink inside the root to a real file with the right bytes
	real := e.write("real.jpg", "bytes-link")
	link := filepath.Join(e.root, "link.jpg")
	_ = os.Symlink(real, link)
	e.add("link", "x", "bytes-link")
	e.entry("link", link, "bytes-link", pruneAt.Add(-time.Hour), "")
	// a symlinked directory inside the root
	ext := t.TempDir()
	_ = os.WriteFile(filepath.Join(ext, "d.jpg"), []byte("bytes-dir"), 0o600)
	_ = os.Symlink(ext, filepath.Join(e.root, "dirlink"))
	e.add("dir", "x", "bytes-dir")
	e.entry("dir", filepath.Join(e.root, "dirlink", "d.jpg"), "bytes-dir", pruneAt.Add(-time.Hour), "")
	// a directory where a file should be
	_ = os.MkdirAll(filepath.Join(e.root, "adir.jpg"), 0o755)
	e.add("isdir", "x", "bytes-isdir")
	e.entry("isdir", filepath.Join(e.root, "adir.jpg"), "bytes-isdir", pruneAt.Add(-time.Hour), "")
	// a sound media file whose sidecar is a symlink out of the root
	m := e.add("xmplink", "bytes-xl", "bytes-xl")
	_ = os.Remove(m + ".xmp")
	target := filepath.Join(t.TempDir(), "keep.xmp")
	_ = os.WriteFile(target, []byte("keep"), 0o600)
	_ = os.Symlink(target, m+".xmp")
	e.seed()
	f := e.fake()
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, p := range []string{outside, outside2, real, filepath.Join(ext, "d.jpg"), target, filepath.Join(e.root, "adir.jpg")} {
		if !exists(p) {
			t.Errorf("%s was deleted", p)
		}
	}
	if l, err := os.Readlink(link); err != nil || l != real {
		t.Errorf("symlink touched: %v %v", l, err)
	}
	if sum.Pruned != 3 || sum.Errors != 6 || sum.MissingFile != 0 {
		t.Errorf("summary %+v, want pruned 3 (pads) errors 6", sum)
	}
	// The sound media went first, then the unsafe sidecar was refused.
	if a := e.get("xmplink"); a.PrunedAt.IsZero() || exists(m) {
		t.Errorf("xmplink: tombstone %v, media left %v", a.PrunedAt, exists(m))
	}
}

func TestPruneTombstonesBeforeUnlinkAndMediaBeforeSidecar(t *testing.T) {
	e := newPEnv(t)
	p := e.add("a", "bytes-a", "bytes-a")
	e.seed()
	f := e.fake()
	var order []string
	old := removeFile
	t.Cleanup(func() { removeFile = old })
	removeFile = func(root, path string) error {
		b, _ := os.ReadFile(e.state)
		if !strings.Contains(string(b), `"prunedAt"`) {
			t.Error("unlink before the tombstone reached the state file")
		}
		order = append(order, filepath.Base(path))
		return unlinkContained(root, path)
	}
	if _, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.Join(order, ",") != "a.jpg,a.jpg.xmp" {
		t.Errorf("order %v", order)
	}
	_ = p
}

// A crash between the stamp and the unlink (or between media and
// sidecar) is finished by the next run, which makes no Immich call and
// never requeues the entry.
func TestPruneCrashAfterStampIsFinishedByRerun(t *testing.T) {
	for name, failOn := range map[string]int{"before media": 1, "between media and sidecar": 2} {
		t.Run(name, func(t *testing.T) {
			e := newPEnv(t)
			for _, id := range []string{"a", "b", "c", "d"} {
				e.add(id, "bytes-"+id, "bytes-"+id)
			}
			e.seed()
			f := e.fake()
			n := 0
			old := removeFile
			t.Cleanup(func() { removeFile = old })
			removeFile = func(root, path string) error {
				n++
				if n == failOn {
					return errors.New("injected crash")
				}
				return unlinkContained(root, path)
			}
			sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
			if code != 1 || sum.Errors != 1 || sum.Pruned != 3 {
				t.Fatalf("crash run: exit %d %+v", code, sum)
			}
			var left string
			for _, id := range []string{"a", "b", "c", "d"} {
				a := e.get(id)
				if a.PrunedAt.IsZero() {
					t.Errorf("%s not tombstoned", id)
				}
				if exists(a.SavedPath) || exists(a.SavedPath+".xmp") {
					left = id
				}
			}
			if left == "" {
				t.Fatal("injected crash left nothing on disk")
			}
			removeFile = unlinkContained
			calls := f.calls.Load()
			sum, code = runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
			if code != 0 || sum.Resumed != 1 || sum.Errors != 0 || sum.Pruned != 0 || sum.Considered != 0 {
				t.Fatalf("rerun: exit %d %+v", code, sum)
			}
			if f.calls.Load() != calls+1 {
				t.Error("rerun should make exactly one bulk check for the leftovers")
			}
			a := e.get(left)
			if exists(a.SavedPath) || exists(a.SavedPath+".xmp") {
				t.Error("rerun left files")
			}
			// Nothing downstream wants it back.
			st, _ := state.Open(context.Background(), e.state)
			defer st.Close()
			if r, err := repairMissing(st, e.root, false, quiet); err != nil || r.Marked != 0 {
				t.Errorf("repair %+v %v", r, err)
			}
			if len(st.Failed()) != 0 || st.Requeue([]string{left}, "x") != 0 {
				t.Error("pruned entry requeued or failed")
			}
			// A third run finds nothing to do.
			st.Close()
			if sum, _ := runPruneCapture(t, e.cfg(f.srv.URL), "--apply"); sum.Resumed != 0 {
				t.Errorf("third run %+v", sum)
			}
		})
	}
}

func TestPruneDeletesNothingWhenImmichFails(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"401": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) },
		"400": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) },
		"short": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"results":[]}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newPEnv(t)
			for _, id := range []string{"a", "b", "c", "d"} {
				e.add(id, "bytes-"+id, "bytes-"+id)
			}
			e.seed()
			srv := httptest.NewServer(h)
			defer srv.Close()
			before := snapshot(t, e.root)
			sum, code := runPruneCapture(t, e.cfg(srv.URL), "--apply")
			if code != 1 || sum.Errors != 4 || sum.Pruned != 0 {
				t.Errorf("exit %d %+v", code, sum)
			}
			if !mapsEqual(before, snapshot(t, e.root)) {
				t.Error("files changed")
			}
			if !e.get("a").PrunedAt.IsZero() {
				t.Error("tombstoned")
			}
		})
	}
}

func TestPruneUnauthorizedStopsLaterBatches(t *testing.T) {
	e := newPEnv(t)
	for i := range verifyBatch + 3 {
		id := "e" + vsum(i)[30:]
		e.add(id, "b"+id, "b"+id)
	}
	e.seed()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(401) }))
	defer srv.Close()
	sum, code := runPruneCapture(t, e.cfg(srv.URL), "--apply")
	if code != 1 || calls != 1 || sum.Errors != verifyBatch+3 || sum.Pruned != 0 {
		t.Errorf("exit %d calls %d %+v", code, calls, sum)
	}
}

func TestPruneGuardsExitCodes(t *testing.T) {
	e := newPEnv(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		e.add(id, "bytes-"+id, "bytes-"+id)
	}
	e.seed()
	f := e.fake()
	cfg := e.cfg(f.srv.URL)
	if _, code := runPruneCapture(t, e.cfg("")); code != 2 {
		t.Errorf("no immich config: %d", code)
	}
	if _, code := runPruneCapture(t, cfg, "stray"); code != 2 {
		t.Errorf("stray arg: %d", code)
	}
	// unmounted: root absent, not a directory, every file missing.
	gone := *cfg
	gone.SaveDir = filepath.Join(t.TempDir(), "nope")
	if _, code := runPruneCapture(t, &gone, "--apply"); code != 1 {
		t.Errorf("absent root: %d", code)
	}
	file := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(file, nil, 0o600)
	gone.SaveDir = file
	if _, code := runPruneCapture(t, &gone, "--apply"); code != 1 {
		t.Errorf("root is a file: %d", code)
	}
	moved := e.root + ".away"
	_ = os.Rename(e.root, moved)
	_ = os.MkdirAll(e.root, 0o755)
	if _, code := runPruneCapture(t, cfg, "--apply"); code != 1 {
		t.Errorf("empty mount: %d", code)
	}
	if f.calls.Load() != 0 {
		t.Error("guard refused after calling Immich")
	}
	_ = os.RemoveAll(e.root)
	_ = os.Rename(moved, e.root)
	// the state lock is held
	st, err := state.Open(context.Background(), e.state)
	if err != nil {
		t.Fatal(err)
	}
	if _, code := runPruneCapture(t, cfg, "--apply"); code != 1 {
		t.Errorf("locked state: %d", code)
	}
	st.Close()
	if _, code := runPruneCapture(t, cfg); code != 0 {
		t.Errorf("clean dry run: %d", code)
	}
}

// End to end: after a real prune, status, repair, backfill, retry-failed
// and requeue all leave the entry alone, and it is counted as pruned.
func TestPruneEndToEndNothingBringsItBack(t *testing.T) {
	e := newPEnv(t)
	for _, id := range []string{"a", "b", "c", "keep"} {
		e.add(id, "bytes-"+id, "bytes-"+id)
	}
	e.entry("keep", e.ents["keep"].SavedPath, "bytes-keep", time.Time{}, "")
	e.seed()
	f := e.fake()
	if _, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply"); code != 0 {
		t.Fatalf("prune exit %d", code)
	}
	out, code := runStatusCapture(t, e.state)
	if code != 0 {
		t.Fatalf("status exit %d", code)
	}
	var got struct{ MissingFile, Pruned, Verified int }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.MissingFile != 0 || got.Pruned != 3 {
		t.Errorf("status %+v, want missingFile 0 pruned 3", got)
	}
	if list, _ := runStatusCapture(t, e.state, "--missing"); strings.TrimSpace(list) != "" {
		t.Errorf("--missing lists %q", list)
	}
	st, _ := state.Open(context.Background(), e.state)
	defer st.Close()
	if r, err := repairMissing(st, e.root, false, quiet); err != nil || r.Marked != 0 || r.Considered != 1 {
		t.Errorf("repair %+v %v", r, err)
	}
	if b := backfillUploadSHA1(st, false, quiet); b.Considered != 0 {
		t.Errorf("backfill %+v", b)
	}
	if len(st.Failed()) != 0 || len(st.VerifyCandidates()) != 1 {
		t.Errorf("failed %v candidates %v", st.Failed(), st.VerifyCandidates())
	}
	if st.Requeue([]string{"a", "b", "c"}, "x") != 0 {
		t.Error("requeued a pruned entry")
	}
	a := vGet(t, st, "a")
	if a.SavedAt.IsZero() || a.SavedPath == "" || a.UploadedAt.IsZero() || a.ImmichAssetID == "" || a.LastError != "" {
		t.Errorf("tombstone lost fields: %+v", a)
	}
}

// A relative save directory gives relative saved paths. The checks and
// the unlink must act on the same file: CWD-relative, as the disk sink
// wrote it.
func TestPruneRelativeSaveDirChecksWhatItDeletes(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	e := newPEnv(t)
	e.root = "assets"
	var rel []string
	for _, id := range []string{"a", "b", "c", "d"} {
		rel = append(rel, e.add(id, "bytes-"+id, "bytes-"+id))
	}
	// Divergence: root/root/ext/x.jpg is a decoy regular file, while
	// root/ext is a symlinked directory out of the root.
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "x.jpg")
	if err := os.WriteFile(outside, []byte("bytes-x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join("assets", "ext")); err != nil {
		t.Fatal(err)
	}
	e.write("assets/ext/x.jpg", "bytes-x")
	e.add("x", "bytes-x", "bytes-x")
	e.entry("x", filepath.Join("assets", "ext", "x.jpg"), "bytes-x", pruneAt.Add(-time.Hour), "")
	e.state = filepath.Join(t.TempDir(), "state.json")
	e.seed()
	f := e.fake()
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if sum.Pruned != 4 || sum.MissingFile != 0 || sum.Errors != 1 || code != 1 {
		t.Fatalf("exit %d %+v", code, sum)
	}
	for _, p := range rel {
		if exists(p) {
			t.Errorf("%s not pruned", p)
		}
	}
	if !exists(outside) {
		t.Error("a file outside the save root was deleted through a symlinked directory")
	}
}

// unlinkContained refuses a directory that became a symlink after the
// containment check.
func TestUnlinkContainedDoesNotFollowASwappedDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assets")
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "x.jpg")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(root, "ext")); err != nil {
		t.Fatal(err)
	}
	if err := removeFile(root, filepath.Join(root, "ext", "x.jpg")); err == nil {
		t.Error("followed a symlinked directory")
	}
	if !exists(outside) {
		t.Error("outside file deleted")
	}
	in := filepath.Join(root, "y", "z.jpg")
	_ = os.MkdirAll(filepath.Dir(in), 0o755)
	_ = os.WriteFile(in, []byte("z"), 0o600)
	if err := unlinkContained(root, in); err != nil || exists(in) {
		t.Errorf("plain unlink: %v exists %v", err, exists(in))
	}
	if err := unlinkContained(root, in); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
}

// A tombstoned entry whose files are still on disk: a dry run counts it
// and deletes nothing; apply finishes it.
func TestPruneDryRunResumeSweepChangesNothing(t *testing.T) {
	e := newPEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		e.add(id, "bytes-"+id, "bytes-"+id)
	}
	p := e.add("t", "bytes-t", "bytes-t")
	a := e.ents["t"]
	a.PrunedAt = pruneAt
	e.ents["t"] = a
	e.seed()
	f := e.fake()
	before := snapshot(t, e.root, filepath.Dir(e.state))
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL))
	if code != 0 || sum.WouldResume != 1 || sum.Resumed != 0 {
		t.Fatalf("exit %d %+v", code, sum)
	}
	if after := snapshot(t, e.root, filepath.Dir(e.state)); !mapsEqual(before, after) {
		t.Fatalf("dry run changed the tree:\n%v\n%v", keys(before), keys(after))
	}
	if sum, _ := runPruneCapture(t, e.cfg(f.srv.URL), "--apply"); sum.Resumed != 1 || exists(p) || exists(p+".xmp") {
		t.Errorf("apply %+v", sum)
	}
}

// A sidecar written after the upload (onto media saved before sidecars
// existed) was never sent to Immich: it stays, and the dry run does not
// count it.
func TestPruneKeepsASidecarNewerThanTheUpload(t *testing.T) {
	e := newPEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		e.add(id, "bytes-"+id, "bytes-"+id)
	}
	p := e.add("late", "bytes-late", "bytes-late")
	if err := os.Chtimes(p+".xmp", pruneAt, pruneAt); err != nil {
		t.Fatal(err)
	}
	e.seed()
	f := e.fake()
	dry, _ := runPruneCapture(t, e.cfg(f.srv.URL))
	want := int64(0)
	for _, id := range []string{"a", "b", "c"} {
		want += int64(len("bytes-"+id) + len("xmp-"+id))
	}
	want += int64(len("bytes-late"))
	if dry.WouldFreeBytes != want {
		t.Errorf("wouldFreeBytes %d, want %d", dry.WouldFreeBytes, want)
	}
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 0 || sum.Pruned != 4 || sum.BytesFreed != want {
		t.Fatalf("exit %d %+v", code, sum)
	}
	if exists(p) || !exists(p+".xmp") {
		t.Errorf("media exists %v, sidecar exists %v", exists(p), exists(p+".xmp"))
	}
}

// Immich only ever got a sidecar for a video: a photo's .xmp is someone
// else's file and survives --apply, in the dry-run byte count too.
func TestPruneKeepsAPhotosXmp(t *testing.T) {
	e := newPEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		e.add(id, "bytes-"+id, "bytes-"+id)
	}
	p := e.add("photo", "bytes-photo", "bytes-photo")
	a := e.ents["photo"]
	a.Source = "feed-image"
	e.ents["photo"] = a
	e.seed()
	f := e.fake()
	want := int64(len("bytes-photo"))
	for _, id := range []string{"a", "b", "c"} {
		want += int64(len("bytes-"+id) + len("xmp-"+id))
	}
	if dry, _ := runPruneCapture(t, e.cfg(f.srv.URL)); dry.WouldFreeBytes != want {
		t.Errorf("wouldFreeBytes %d, want %d", dry.WouldFreeBytes, want)
	}
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 0 || sum.Pruned != 4 || sum.BytesFreed != want {
		t.Fatalf("exit %d %+v want bytes %d", code, sum, want)
	}
	if exists(p) || !exists(p+".xmp") {
		t.Errorf("media exists %v, sidecar exists %v", exists(p), exists(p+".xmp"))
	}
}

// Trashed in Immich after the prune, then the file copied back: the
// resume sweep must not delete the only copy, and the entry is unpruned.
func TestPruneResumeKeepsAFileWhoseAssetIsTrashed(t *testing.T) {
	e := newPEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		e.add(id, "bytes-"+id, "bytes-"+id)
	}
	p := e.add("t", "bytes-t", "bytes-t")
	e.table[shaOf("bytes-t")] = vKnown{id: "imm-t", trashed: true}
	a := e.ents["t"]
	a.PrunedAt = pruneAt
	e.ents["t"] = a
	e.seed()
	f := e.fake()
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 0 || sum.ResumeKept != 1 || sum.Resumed != 0 {
		t.Fatalf("exit %d %+v", code, sum)
	}
	if !exists(p) || !exists(p+".xmp") {
		t.Error("the copied-back file was deleted")
	}
	if g := e.get("t"); !g.PrunedAt.IsZero() || g.VerifyResult != "trashed" {
		t.Errorf("entry not unpruned and demoted: %+v", g)
	}
	// And it stays: the next run does not touch it either.
	if sum, _ := runPruneCapture(t, e.cfg(f.srv.URL), "--apply"); sum.Resumed != 0 || !exists(p) {
		t.Errorf("second run %+v", sum)
	}
}

// A kill after the batch's single state write and before any unlink:
// every verified entry is tombstoned with its file on disk, and the
// next run finishes them all.
func TestPruneCrashBetweenFlushAndUnlinkIsResumed(t *testing.T) {
	e := newPEnv(t)
	ids := []string{"a", "b", "c", "d"}
	for _, id := range ids {
		e.add(id, "bytes-"+id, "bytes-"+id)
	}
	e.seed()
	f := e.fake()
	old := removeFile
	t.Cleanup(func() { removeFile = old })
	removeFile = func(string, string) error { panic("kill") }
	st, err := state.Open(context.Background(), e.state)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		pruneEntries(context.Background(), st, f.client(), e.root, false, pruneAt, quiet)
		t.Error("no crash")
	}()
	b, _ := os.ReadFile(e.state)
	if n := strings.Count(string(b), `"prunedAt"`); n != len(ids) {
		t.Errorf("%d of %d tombstones durable at the crash", n, len(ids))
	}
	_ = st.Close()
	for _, id := range ids {
		if !exists(e.ents[id].SavedPath) {
			t.Errorf("%s: file gone", id)
		}
	}
	removeFile = unlinkContained
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 0 || sum.Resumed != len(ids) {
		t.Fatalf("rerun: exit %d %+v", code, sum)
	}
	for _, id := range ids {
		if exists(e.ents[id].SavedPath) {
			t.Errorf("%s: left", id)
		}
	}
}

// R3: media gone, sidecar left, asset trashed. The tombstone and the
// sidecar stay, and repair finds nothing to re-download.
func TestPruneResumeMediaAbsentKeepsTombstone(t *testing.T) {
	e := newPEnv(t)
	p := e.add("t", "bytes-t", "bytes-t")
	e.table[shaOf("bytes-t")] = vKnown{id: "imm-t", trashed: true}
	a := e.ents["t"]
	a.PrunedAt = pruneAt
	e.ents["t"] = a
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	e.seed()
	old := removeFile
	t.Cleanup(func() { removeFile = old })
	removeFile = func(root, q string) error {
		if q == p {
			t.Error("removeFile called on the absent media path")
		}
		return old(root, q)
	}
	f := e.fake()
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 0 || sum.ResumeKept != 1 || sum.Resumed != 0 {
		t.Fatalf("exit %d %+v", code, sum)
	}
	if !exists(p + ".xmp") {
		t.Error("sidecar deleted")
	}
	if g := e.get("t"); g.PrunedAt.IsZero() {
		t.Errorf("tombstone cleared with the media absent: %+v", g)
	}
	st, err := state.Open(context.Background(), e.state)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if r, err := repairMissing(st, e.root, true, quiet); err != nil || r.Marked != 0 {
		t.Errorf("repair dry run: %+v %v", r, err)
	}
}

// R3 verified: media gone, sidecar left, asset held: the sidecar goes.
func TestPruneResumeMediaAbsentVerifiedRemovesSidecar(t *testing.T) {
	e := newPEnv(t)
	p := e.add("t", "bytes-t", "bytes-t")
	a := e.ents["t"]
	a.PrunedAt = pruneAt
	e.ents["t"] = a
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	e.seed()
	old := removeFile
	t.Cleanup(func() { removeFile = old })
	removeFile = func(root, q string) error {
		if q == p {
			t.Error("removeFile called on the absent media path")
		}
		return old(root, q)
	}
	f := e.fake()
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 0 || sum.Resumed != 1 || exists(p+".xmp") || e.get("t").PrunedAt.IsZero() {
		t.Fatalf("exit %d %+v", code, sum)
	}
}

// R2: media present, gate fails on the hash: kept, un-pruned, not an
// error, and counted once (not again by the main pass).
func TestPruneResumeHashMismatchUnprunesOnceCounted(t *testing.T) {
	e := newPEnv(t)
	p := e.add("t", "NEW-BYTES", "bytes-t")
	a := e.ents["t"]
	a.PrunedAt = pruneAt
	e.ents["t"] = a
	e.seed()
	f := e.fake()
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	if code != 0 || sum.ResumeKept != 1 || sum.Errors != 0 || sum.Considered != 0 || sum.NotVerified != 0 || sum.LocalDiffers != 0 {
		t.Fatalf("exit %d %+v", code, sum)
	}
	if !exists(p) || !exists(p+".xmp") || !e.get("t").PrunedAt.IsZero() {
		t.Error("file lost or entry still pruned")
	}
}

// R5: a dry-run resume reports the bytes it would free.
func TestPruneDryRunResumeWouldFreeBytes(t *testing.T) {
	e := newPEnv(t)
	e.add("t", "bytes-t", "bytes-t")
	a := e.ents["t"]
	a.PrunedAt = pruneAt
	e.ents["t"] = a
	e.seed()
	f := e.fake()
	sum, _ := runPruneCapture(t, e.cfg(f.srv.URL))
	if want := int64(len("bytes-t") + len("xmp-t")); sum.WouldResume != 1 || sum.WouldFreeBytes != want {
		t.Fatalf("%+v want %d", sum, want)
	}
}

// R4: a failing state write (read-only state dir after Open) stops the
// run with every file kept, Errors counted and exit 1.
func flushFailEnv(t *testing.T, setup func(e *pEnv)) (*pEnv, int, pruneSummary) {
	e := newPEnv(t)
	setup(e)
	e.seed()
	f := e.fake()
	dir := filepath.Dir(e.state)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply")
	return e, code, sum
}

func TestPruneFlushFailureKeepsEveryFile(t *testing.T) {
	cases := map[string]func(e *pEnv){
		"post-stamp": func(e *pEnv) { e.add("a", "bytes-a", "bytes-a") },
		"demote": func(e *pEnv) {
			e.add("a", "bytes-a", "bytes-a")
			e.table[shaOf("bytes-a")] = vKnown{id: "imm-a", trashed: true}
		},
		"resume-clear": func(e *pEnv) {
			e.add("a", "bytes-a", "bytes-a")
			e.table[shaOf("bytes-a")] = vKnown{id: "imm-a", trashed: true}
			x := e.ents["a"]
			x.PrunedAt = pruneAt
			e.ents["a"] = x
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e, code, sum := flushFailEnv(t, setup)
			if code != 1 || sum.Errors == 0 {
				t.Errorf("exit %d %+v", code, sum)
			}
			for _, a := range e.ents {
				if !exists(a.SavedPath) || !exists(a.SavedPath+".xmp") {
					t.Errorf("file lost: %s", a.SavedPath)
				}
			}
		})
	}
}

// A dry run over leftovers whose gate fails (trashed, hash mismatch)
// changes nothing on disk or in state.
func TestPruneDryRunResumeGateFailChangesNothing(t *testing.T) {
	e := newPEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		e.add(id, "bytes-"+id, "bytes-"+id)
	}
	e.add("tr", "bytes-tr", "bytes-tr")
	e.table[shaOf("bytes-tr")] = vKnown{id: "imm-tr", trashed: true}
	e.add("mm", "NEW-BYTES", "bytes-mm")
	for _, id := range []string{"tr", "mm"} {
		a := e.ents[id]
		a.PrunedAt = pruneAt
		e.ents[id] = a
	}
	e.seed()
	f := e.fake()
	before := snapshot(t, e.root, filepath.Dir(e.state))
	sum, code := runPruneCapture(t, e.cfg(f.srv.URL))
	if code != 0 || sum.ResumeKept != 2 {
		t.Fatalf("exit %d %+v", code, sum)
	}
	if after := snapshot(t, e.root, filepath.Dir(e.state)); !mapsEqual(before, after) {
		t.Fatalf("dry run changed the tree:\n%v\n%v", keys(before), keys(after))
	}
}

// Media present at scan time but gone by the time Immich answers: the
// tombstone stays, so repair never re-queues it.
func TestPruneResumeMediaGoneDuringRecheckKeepsTombstone(t *testing.T) {
	e := newPEnv(t)
	p := e.add("t", "bytes-t", "bytes-t")
	e.table[shaOf("bytes-t")] = vKnown{id: "imm-t", trashed: true}
	a := e.ents["t"]
	a.PrunedAt = pruneAt
	e.ents["t"] = a
	e.seed()
	f := e.fake()
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = os.Remove(p)
		inner.ServeHTTP(w, r)
	})
	if _, code := runPruneCapture(t, e.cfg(f.srv.URL), "--apply"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if g := e.get("t"); g.PrunedAt.IsZero() {
		t.Errorf("tombstone cleared with the media gone: %+v", g)
	}
}
