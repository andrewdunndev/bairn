package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/dunn.dev/bairn/api/immich"
	"gitlab.com/dunn.dev/bairn/internal/asset"
	"gitlab.com/dunn.dev/bairn/internal/config"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
	"golang.org/x/sys/unix"
)

// pruneSummary is what `prune` prints. Considered counts the uploaded,
// saved, unpruned entries; each is counted once, under Pruned (apply),
// WouldPrune (dry run), one skip reason or Errors. An entry tombstoned
// whose unlink failed is under Errors and finished by the next run.
type pruneSummary struct {
	DryRun         bool  `json:"dryRun"`
	Considered     int   `json:"considered"`
	Pruned         int   `json:"pruned"`
	WouldPrune     int   `json:"wouldPrune"`
	NotVerified    int   `json:"notVerified"`
	ReverifyFailed int   `json:"reverifyFailed"`
	LocalDiffers   int   `json:"localDiffers"`
	MissingFile    int   `json:"missingFile"`
	Errors         int   `json:"errors"`
	BytesFreed     int64 `json:"bytesFreed"`
	WouldFreeBytes int64 `json:"wouldFreeBytes"`
	// Resumed counts entries already tombstoned whose file a crashed run
	// left behind and this run removed; WouldResume is the dry-run count.
	Resumed     int `json:"resumed"`
	WouldResume int `json:"wouldResume"`
	// ResumeKept counts tombstoned entries the resume sweep did not
	// finish: with the media present (Immich no longer holds the asset,
	// or the file is not the uploaded bytes) the entry is unpruned and
	// the file stays; with the media absent the tombstone and sidecar
	// stay. Each is counted here and not again in the main pass.
	ResumeKept int `json:"resumeKept"`
}

var (
	errNoFile = errors.New("no such file")
	errRefuse = errors.New("refused")
)

// removeFile is the one place prune deletes; tests replace it. It takes
// the resolved absolute path and the save root.
var removeFile = unlinkContained

// flushState writes the state before any unlink; a test hook.
var flushState = func(st *state.Store) error { return st.Flush() }

// stillPresent reports whether path exists now, without following a
// final symlink.
func stillPresent(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// unlinkContained deletes p with no symlink followed anywhere below root:
// each directory is opened O_NOFOLLOW relative to the one before and the
// file is unlinked relative to the last, so a directory swapped for a
// symlink after the check is refused, not followed.
func unlinkContained(root, p string) error {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || !sink.Within(root, p) {
		return fmt.Errorf("%w: %s is outside %s", errRefuse, p, root)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: root, Err: err}
	}
	for _, part := range parts[:len(parts)-1] {
		nfd, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return &fs.PathError{Op: "openat", Path: part, Err: err}
		}
		fd = nfd
	}
	defer unix.Close(fd)
	name := parts[len(parts)-1]
	if err := unix.Unlinkat(fd, name, 0); err != nil {
		return &fs.PathError{Op: "unlinkat", Path: p, Err: err}
	}
	return nil
}

// runPrune implements `bairn prune [--apply]`.
func runPrune(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	fl := flag.NewFlagSet("prune", flag.ExitOnError)
	apply := fl.Bool("apply", false, "delete the verified local copies (default: a dry run that changes nothing)")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	if fl.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: bairn prune [--apply]")
		return 2
	}
	if cfg.ImmichBaseURL == "" || cfg.ImmichAPIKey == "" {
		fmt.Fprintln(os.Stderr, "prune: IMMICH_BASE_URL and IMMICH_API_KEY are required")
		return 2
	}
	// Open takes the exclusive state lock, so this cannot run beside a fetch.
	st, err := state.Open(ctx, cfg.StatePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "state:", err)
		return 1
	}
	if gerr := guardSaveRoot(st, cfg.SaveDir); gerr != nil {
		_ = st.Close() // nothing was changed
		fmt.Fprintln(os.Stderr, "prune: refusing:", gerr)
		return 1
	}
	ic := immich.New(cfg.ImmichBaseURL, cfg.ImmichAPIKey, immich.WithLogger(logger))
	sum := pruneEntries(ctx, st, ic, cfg.SaveDir, !*apply, time.Now().UTC(), logger)
	if err := st.Close(); err != nil {
		logger.Error("prune: write state", "err", err)
		sum.Errors++
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(sum)
	if sum.Errors > 0 {
		return 1
	}
	return 0
}

// guardSaveRoot refuses when the save root is unset, absent, not a
// directory, or every unpruned saved file is missing while more than
// mountGoneMin are expected: an unmounted volume looks exactly like that.
func guardSaveRoot(st *state.Store, root string) error {
	if root == "" {
		return errors.New("save directory is not configured")
	}
	fi, err := os.Stat(root)
	switch {
	case err != nil:
		return fmt.Errorf("save directory %s: %w", root, err)
	case !fi.IsDir():
		return fmt.Errorf("save directory %s is not a directory", root)
	}
	files := st.SavedFiles()
	missing := 0
	for _, f := range files {
		if _, err := statSaved(f.SavedPath); errors.Is(err, errNoSavedFile) {
			missing++
		}
	}
	if len(files) > mountGoneMin && missing == len(files) {
		return fmt.Errorf("all %d saved files are missing under %s: is the volume mounted?", len(files), root)
	}
	return nil
}

// lstatContained resolves p (made absolute against the working directory,
// as every other open of it is) and returns the resolved path with its
// Lstat; callers act on that path only. It refuses
// (errRefuse) a path outside root, any symlink on the way below root, a
// non-directory parent, and a final element that is not a regular file.
// A component that does not exist is errNoFile.
func lstatContained(root, p string) (string, fs.FileInfo, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", nil, err
	}
	if p, err = filepath.Abs(p); err != nil {
		return "", nil, err
	}
	if !sink.Within(root, p) || p == root {
		return "", nil, fmt.Errorf("%w: %s is outside %s", errRefuse, p, root)
	}
	rel, _ := filepath.Rel(root, p)
	parts := strings.Split(rel, string(filepath.Separator))
	cur := root
	var fi fs.FileInfo
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		var err error
		fi, err = os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, errNoFile
		}
		if err != nil {
			return "", nil, err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			return "", nil, fmt.Errorf("%w: %s is a symlink", errRefuse, cur)
		case i < len(parts)-1 && !fi.IsDir():
			return "", nil, fmt.Errorf("%w: %s is not a directory", errRefuse, cur)
		}
	}
	if !fi.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%w: %s is not a regular file", errRefuse, p)
	}
	return p, fi, nil
}

// pruneEntries is the whole run. dry makes the Immich calls but writes
// no state and removes no file. In apply mode, per batch: PrunedAt is
// stamped on every entry that passed, the state flushed once, then each
// media is removed, then its .xmp. A batch whose re-verify fails deletes
// nothing; an auth failure,
// a cancelled context or a failed state write stops the run, counting
// every entry not yet decided as an error.
func pruneEntries(ctx context.Context, st *state.Store, ic bulkChecker, root string, dry bool, at time.Time, logger *slog.Logger) pruneSummary {
	sum := pruneSummary{DryRun: dry}
	skip, abort := resumePruned(ctx, st, ic, root, dry, at, &sum, logger)
	if abort {
		return sum
	}

	var todo []state.PruneEntry
	for _, e := range st.PruneCandidates() {
		if skip[e.ID] { // un-pruned by the resume sweep: counted there
			continue
		}
		sum.Considered++
		// Only an entry the last verify confirmed, and that holds both
		// halves of the question, goes to the re-verify.
		if e.ImmichVerifiedAt.IsZero() || e.VerifyResult != "" || e.UploadSHA1 == "" || e.ImmichAssetID == "" {
			sum.NotVerified++
			continue
		}
		todo = append(todo, e)
	}
	for i := 0; i < len(todo); i += verifyBatch {
		batch := todo[i:min(i+verifyBatch, len(todo))]
		abort, err := pruneBatch(ctx, st, ic, root, batch, dry, at, &sum, logger)
		if err != nil {
			logger.Error("prune: batch failed; nothing deleted for it", "err", err)
		}
		if abort {
			sum.Errors += len(todo) - i - len(batch)
			break
		}
	}
	return sum
}

// pruneBatch re-verifies one batch, then decides each entry. It returns
// abort for an error no later batch can survive. Entries it could not
// decide are counted under Errors.
func pruneBatch(ctx context.Context, st *state.Store, ic bulkChecker, root string, batch []state.PruneEntry, dry bool, at time.Time, sum *pruneSummary, logger *slog.Logger) (abort bool, err error) {
	outcomes, err := recheck(ctx, ic, batch)
	if err != nil {
		sum.Errors += len(batch)
		return errors.Is(err, immich.ErrUnauthorized) || ctx.Err() != nil, err
	}
	// A failed re-verify demotes the stamp, so the entry shows as
	// failed in status and the next prune does not re-ask.
	demoted := false
	for i, e := range batch {
		if outcomes[i] != resVerified {
			sum.ReverifyFailed++
			if !dry {
				st.StampVerify(e.ID, outcomes[i], at)
				demoted = true
			}
		}
	}
	if demoted {
		if err := flushState(st); err != nil {
			// The demotions are not durable: the whole batch is
			// undecided, so each entry moves to Errors, once.
			sum.ReverifyFailed -= countFailed(outcomes)
			sum.Errors += len(batch)
			return true, fmt.Errorf("write state: %w", err)
		}
	}
	var pend []pending
	for i, e := range batch {
		if outcomes[i] != resVerified {
			continue
		}
		if p, ok := checkOne(root, e, dry, sum, logger); ok {
			pend = append(pend, p)
		}
	}
	if dry || len(pend) == 0 {
		return false, nil
	}
	// Tombstone the batch and make it durable before any unlink: a kill
	// after this leaves pruned entries with their files, which the next
	// run re-verifies and finishes; a kill before leaves ordinary
	// verified entries.
	stamped := pend[:0]
	for _, p := range pend {
		if st.MarkPruned(p.e.ID, at) {
			stamped = append(stamped, p)
		} else {
			sum.Errors++
		}
	}
	if err := flushState(st); err != nil {
		sum.Errors += len(stamped)
		return true, fmt.Errorf("write state: %w", err)
	}
	for _, p := range stamped {
		freed, rerr := removeEntryFiles(root, p.path, p.size, p.e)
		sum.BytesFreed += freed
		if rerr != nil {
			logger.Error("prune: unlink; the entry is tombstoned and the next run finishes it", "id", p.e.ID, "err", rerr)
			sum.Errors++
			continue
		}
		sum.Pruned++
	}
	return false, nil
}

// recheck asks Immich one bulk question about the entries and returns
// each one's outcome, in order.
func recheck(ctx context.Context, ic bulkChecker, es []state.PruneEntry) ([]string, error) {
	items := make([]immich.CheckItem, len(es))
	for i, e := range es {
		items[i] = immich.CheckItem{ID: e.ID, Checksum: e.UploadSHA1}
	}
	res, err := ic.BulkUploadCheck(ctx, items)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]immich.CheckResult, len(res))
	for _, r := range res {
		byID[r.ID] = r
	}
	if len(byID) != len(es) {
		return nil, fmt.Errorf("reply covers %d of %d entries", len(byID), len(es))
	}
	out := make([]string, len(es))
	for i, e := range es {
		r, ok := byID[e.ID]
		if !ok {
			return nil, errors.New("reply is missing a requested entry")
		}
		out[i] = classify(state.VerifyCandidate{ID: e.ID, UploadSHA1: e.UploadSHA1, ImmichAssetID: e.ImmichAssetID}, r)
	}
	return out, nil
}

func countFailed(outcomes []string) int {
	n := 0
	for _, o := range outcomes {
		if o != resVerified {
			n++
		}
	}
	return n
}

// pending is a re-verified entry whose local file passed every check.
type pending struct {
	e    state.PruneEntry
	path string
	size int64
}

// checkOne applies the local checks to a re-verified entry. ok reports
// an entry to delete (apply only); a dry run counts it as wouldPrune.
func checkOne(root string, e state.PruneEntry, dry bool, sum *pruneSummary, logger *slog.Logger) (p pending, ok bool) {
	if e.SavedPath == "" {
		sum.MissingFile++
		return p, false
	}
	path, fi, err := lstatContained(root, e.SavedPath)
	switch {
	case errors.Is(err, errNoFile):
		sum.MissingFile++
		return p, false
	case err != nil:
		logger.Error("prune: refusing path", "id", e.ID, "err", err)
		sum.Errors++
		return p, false
	}
	got, err := hashFile(path)
	if err != nil {
		logger.Error("prune: hash", "id", e.ID, "err", err)
		sum.Errors++
		return p, false
	}
	if got != e.UploadSHA1 {
		sum.LocalDiffers++
		return p, false
	}
	if dry {
		sum.WouldPrune++
		sum.WouldFreeBytes += fi.Size()
		if _, xfi, err := sidecar(root, path, e); err == nil && xfi != nil {
			sum.WouldFreeBytes += xfi.Size()
		}
		return p, false
	}
	return pending{e: e, path: path, size: fi.Size()}, true
}

// sidecar is the .xmp beside the resolved media path, when prune should
// delete it: bairn uploaded it to Immich (only a feed-video's sidecar is
// sent; any other .xmp is someone else's file), it is present and
// contained, and not newer than the upload. A newer one was written
// after Immich last saw the entry, so it is the only copy and stays
// (nil FileInfo, nil error), as does a missing one (errNoFile is folded
// into the same).
func sidecar(root, media string, e state.PruneEntry) (string, fs.FileInfo, error) {
	if e.Source != string(asset.SourceFeedVideo) {
		return "", nil, nil
	}
	xp, xfi, err := lstatContained(root, media+".xmp")
	switch {
	case errors.Is(err, errNoFile):
		return "", nil, nil
	case err != nil:
		return "", nil, err
	case xfi.ModTime().After(e.UploadedAt):
		return "", nil, nil
	}
	return xp, xfi, nil
}

// removeEntryFiles deletes the media (a resolved path from
// lstatContained), then its gated .xmp sidecar, each unlinked without
// following a symlink. A missing file is fine. It returns the bytes freed
// and the first error.
func removeEntryFiles(root, path string, mediaSize int64, e state.PruneEntry) (int64, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	if err := removeFile(root, path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	freed, err := removeSidecar(root, path, e)
	return mediaSize + freed, err
}

// removeSidecar deletes only the gated sidecar of the media at path; it
// never touches the media path itself.
func removeSidecar(root, path string, e state.PruneEntry) (int64, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	xp, xfi, err := sidecar(root, path, e)
	if err != nil || xfi == nil {
		return 0, err
	}
	if err := removeFile(root, xp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	return xfi.Size(), nil
}

// resumePruned finishes a crashed run, under one invariant: an entry is
// un-pruned only when its media is on disk, and media is unlinked only
// after the full gate (contained, regular, not a symlink, hash equal to
// the uploaded bytes, the same-run bulk check still finding the asset).
// Media present and the gate passes: media, then the gated sidecar, go.
// Media present and the gate fails: the file stays and PrunedAt is
// cleared (the verify result is stamped when Immich answered), counted
// under ResumeKept. Media absent: the tombstone always stays; a verified
// run removes the gated sidecar, otherwise the sidecar stays, counted
// under ResumeKept. skip holds the entries un-pruned here, so the main
// pass does not count them again. abort reports a failed state write or
// an unusable Immich.
func resumePruned(ctx context.Context, st *state.Store, ic bulkChecker, root string, dry bool, at time.Time, sum *pruneSummary, logger *slog.Logger) (skip map[string]bool, abort bool) {
	type left struct {
		e    state.PruneEntry
		path string
		fi   fs.FileInfo // nil: media absent, only a sidecar remains
	}
	skip = map[string]bool{}
	var lefts []left
	for _, e := range st.PrunedEntries() {
		if e.SavedPath == "" {
			continue
		}
		path, fi, err := lstatContained(root, e.SavedPath)
		switch {
		case errors.Is(err, errNoFile):
			fi = nil
			path, _ = filepath.Abs(e.SavedPath)
		case err != nil:
			logger.Error("prune: pruned entry, refusing path", "id", e.ID, "err", err)
			sum.Errors++
			continue
		}
		if _, xfi, xerr := sidecar(root, path, e); fi == nil && xfi == nil && xerr == nil {
			continue
		}
		lefts = append(lefts, left{e, path, fi})
	}
	cleared := false
	for i := 0; i < len(lefts); i += verifyBatch {
		batch := lefts[i:min(i+verifyBatch, len(lefts))]
		es := make([]state.PruneEntry, len(batch))
		for j, l := range batch {
			es[j] = l.e
		}
		outcomes, err := recheck(ctx, ic, es)
		if err != nil {
			logger.Error("prune: resume re-verify failed; leftovers kept", "err", err)
			sum.Errors += len(lefts) - i
			return skip, errors.Is(err, immich.ErrUnauthorized) || ctx.Err() != nil
		}
		for j, l := range batch {
			e := l.e
			verified := outcomes[j] == resVerified
			gate := verified
			if gate && l.fi != nil {
				got, herr := hashFile(l.path)
				gate = herr == nil && got == e.UploadSHA1
			}
			if !gate {
				sum.ResumeKept++
				// Un-prune only while the media is still on disk: it may
				// have gone during the re-verify round trip.
				if l.fi != nil && !dry && stillPresent(l.path) {
					st.ClearPruned(e.ID)
					if !verified {
						st.StampVerify(e.ID, outcomes[j], at)
					}
					skip[e.ID] = true
					cleared = true
				}
				continue
			}
			var size int64
			if l.fi != nil {
				size = l.fi.Size()
			}
			if dry {
				if _, xfi, _ := sidecar(root, l.path, e); xfi != nil {
					size += xfi.Size()
				}
				sum.WouldResume++
				sum.WouldFreeBytes += size
				continue
			}
			var freed int64
			var rerr error
			if l.fi != nil {
				freed, rerr = removeEntryFiles(root, l.path, size, e)
			} else {
				freed, rerr = removeSidecar(root, l.path, e)
			}
			if rerr != nil {
				logger.Error("prune: unlink", "id", e.ID, "err", rerr)
				sum.Errors++
				continue
			}
			sum.Resumed++
			sum.BytesFreed += freed
		}
	}
	if cleared {
		if err := flushState(st); err != nil {
			logger.Error("prune: write state", "err", err)
			sum.Errors++
			return skip, true
		}
	}
	return skip, false
}
