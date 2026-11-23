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
}

var (
	errNoFile = errors.New("no such file")
	errRefuse = errors.New("refused")
)

// removeFile is the one place prune deletes; tests replace it. It takes
// the resolved absolute path and the save root.
var removeFile = unlinkContained

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
// no state and removes no file. In apply mode, per entry: PrunedAt is
// stamped and the state flushed, then the media is removed, then its
// .xmp. A batch whose re-verify fails deletes nothing; an auth failure,
// a cancelled context or a failed state write stops the run, counting
// every entry not yet decided as an error.
func pruneEntries(ctx context.Context, st *state.Store, ic bulkChecker, root string, dry bool, at time.Time, logger *slog.Logger) pruneSummary {
	sum := pruneSummary{DryRun: dry}
	resumePruned(st, root, dry, &sum, logger)

	var todo []state.PruneEntry
	for _, e := range st.PruneCandidates() {
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
	items := make([]immich.CheckItem, len(batch))
	for i, e := range batch {
		items[i] = immich.CheckItem{ID: e.ID, Checksum: e.UploadSHA1}
	}
	res, err := ic.BulkUploadCheck(ctx, items)
	if err != nil {
		sum.Errors += len(batch)
		return errors.Is(err, immich.ErrUnauthorized) || ctx.Err() != nil, err
	}
	byID := make(map[string]immich.CheckResult, len(res))
	for _, r := range res {
		byID[r.ID] = r
	}
	if len(byID) != len(batch) {
		sum.Errors += len(batch)
		return false, fmt.Errorf("reply covers %d of %d entries", len(byID), len(batch))
	}
	outcomes := make([]string, len(batch))
	for i, e := range batch {
		r, ok := byID[e.ID]
		if !ok {
			sum.Errors += len(batch)
			return false, errors.New("reply is missing a requested entry")
		}
		outcomes[i] = classify(state.VerifyCandidate{ID: e.ID, UploadSHA1: e.UploadSHA1, ImmichAssetID: e.ImmichAssetID}, r)
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
		if err := st.Flush(); err != nil {
			sum.Errors += len(batch) - countFailed(outcomes)
			return true, fmt.Errorf("write state: %w", err)
		}
	}
	for i, e := range batch {
		if outcomes[i] != resVerified {
			continue
		}
		if abort, err := pruneOne(st, root, e, dry, at, sum, logger); abort {
			// Entries after this one in the batch are undecided.
			for _, rest := range outcomes[i+1:] {
				if rest == resVerified {
					sum.Errors++
				}
			}
			return true, err
		}
	}
	return false, nil
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

// pruneOne applies the local checks to a re-verified entry and, unless
// dry, tombstones then deletes it. abort reports a failed state write.
func pruneOne(st *state.Store, root string, e state.PruneEntry, dry bool, at time.Time, sum *pruneSummary, logger *slog.Logger) (abort bool, err error) {
	if e.SavedPath == "" {
		sum.MissingFile++
		return false, nil
	}
	path, fi, err := lstatContained(root, e.SavedPath)
	switch {
	case errors.Is(err, errNoFile):
		sum.MissingFile++
		return false, nil
	case err != nil:
		logger.Error("prune: refusing path", "id", e.ID, "err", err)
		sum.Errors++
		return false, nil
	}
	got, err := hashFile(path)
	if err != nil {
		logger.Error("prune: hash", "id", e.ID, "err", err)
		sum.Errors++
		return false, nil
	}
	if got != e.UploadSHA1 {
		sum.LocalDiffers++
		return false, nil
	}
	if dry {
		sum.WouldPrune++
		sum.WouldFreeBytes += fi.Size()
		if _, xfi, err := sidecar(root, path, e.UploadedAt); err == nil && xfi != nil {
			sum.WouldFreeBytes += xfi.Size()
		}
		return false, nil
	}
	// Tombstone first and make it durable: a kill after this leaves a
	// pruned entry with its file, which the next run finishes; a kill
	// before leaves an ordinary verified entry.
	if !st.MarkPruned(e.ID, at) {
		sum.Errors++
		return false, nil
	}
	if err := st.Flush(); err != nil {
		sum.Errors++
		return true, fmt.Errorf("write state: %w", err)
	}
	freed, rerr := removeEntryFiles(root, path, fi.Size(), e.UploadedAt)
	sum.BytesFreed += freed
	if rerr != nil {
		logger.Error("prune: unlink; the entry is tombstoned and the next run finishes it", "id", e.ID, "err", rerr)
		sum.Errors++
		return false, nil
	}
	sum.Pruned++
	return false, nil
}

// sidecar is the .xmp beside the resolved media path, when prune should
// delete it: present, contained, and not newer than the upload. A newer
// one was written after Immich last saw the entry, so it is the only
// copy and stays (nil FileInfo, nil error), as does a missing one
// (errNoFile is folded into the same).
func sidecar(root, media string, uploadedAt time.Time) (string, fs.FileInfo, error) {
	xp, xfi, err := lstatContained(root, media+".xmp")
	switch {
	case errors.Is(err, errNoFile):
		return "", nil, nil
	case err != nil:
		return "", nil, err
	case xfi.ModTime().After(uploadedAt):
		return "", nil, nil
	}
	return xp, xfi, nil
}

// removeEntryFiles deletes the media (a resolved path from
// lstatContained), then its .xmp sidecar, each unlinked without following
// a symlink. A missing file is fine. It returns the bytes freed and the
// first error.
func removeEntryFiles(root, path string, mediaSize int64, uploadedAt time.Time) (int64, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	var freed int64
	if err := removeFile(root, path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return freed, err
	}
	freed += mediaSize
	xp, xfi, err := sidecar(root, path, uploadedAt)
	if err != nil || xfi == nil {
		return freed, err
	}
	if err := removeFile(root, xp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return freed, err
	}
	return freed + xfi.Size(), nil
}

// resumePruned finishes a crashed run: an entry with PrunedAt set whose
// media or sidecar is still on disk is deleted, under the same checks
// (contained, no symlink, regular, and the media still the bytes
// bairn uploaded). It never touches the state and never re-downloads.
func resumePruned(st *state.Store, root string, dry bool, sum *pruneSummary, logger *slog.Logger) {
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
		if fi != nil {
			got, err := hashFile(path)
			if err != nil || got != e.UploadSHA1 {
				logger.Error("prune: pruned entry's file is not the uploaded bytes; kept", "id", e.ID, "err", err)
				sum.Errors++
				continue
			}
		}
		if _, xfi, xerr := sidecar(root, path, e.UploadedAt); fi == nil && xfi == nil && xerr == nil {
			continue
		}
		if dry {
			sum.WouldResume++
			continue
		}
		var size int64
		if fi != nil {
			size = fi.Size()
		}
		freed, rerr := removeEntryFiles(root, path, size, e.UploadedAt)
		if rerr != nil {
			logger.Error("prune: unlink", "id", e.ID, "err", rerr)
			sum.Errors++
			continue
		}
		sum.Resumed++
		sum.BytesFreed += freed
	}
}
