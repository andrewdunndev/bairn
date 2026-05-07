package sink

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/exif"
)

// DefaultFilenamePattern is jacobbunk-style. Source values like
// "feed-image" already carry the type prefix, so a literal extra
// "feed-" would double up.
const DefaultFilenamePattern = "{{.Source}}-%Y-%m-%d_%H-%M-%S-{{.ID}}.{{.Ext}}"

// DefaultDirPattern is per-day buckets.
const DefaultDirPattern = "%Y-%m-%d"

// Disk writes assets to a configured directory tree. EXIF
// reinjection happens after the file is in its final location.
type Disk struct {
	root            string
	filenamePattern string
	dirPattern      string
}

// NewDisk constructs a Disk sink rooted at root. Empty patterns
// fall back to DefaultFilenamePattern and DefaultDirPattern.
func NewDisk(root, filenamePattern, dirPattern string) (*Disk, error) {
	if root == "" {
		return nil, fmt.Errorf("sink: Disk root must be non-empty")
	}
	if filenamePattern == "" {
		filenamePattern = DefaultFilenamePattern
	}
	if dirPattern == "" {
		dirPattern = DefaultDirPattern
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("sink: mkdir %s: %w", root, err)
	}
	return &Disk{root: root, filenamePattern: filenamePattern, dirPattern: dirPattern}, nil
}

// Name implements Sink.
func (Disk) Name() string { return "disk" }

// Put copies (or renames, when SourcePath is on the same volume)
// the temp file to the resolved destination path, fsyncs, sets
// mtime from FileCreatedAt, then runs EXIF reinjection. Returns a
// Receipt with the final on-disk path. Idempotent: if the
// destination already exists with the same SHA1, returns
// status=duplicate without rewriting.
func (d *Disk) Put(ctx context.Context, in PutInput) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	subdir, err := renderPattern(d.dirPattern, in)
	if err != nil {
		return Receipt{}, fmt.Errorf("sink: render dir pattern: %w", err)
	}
	name, err := renderPattern(d.filenamePattern, in)
	if err != nil {
		return Receipt{}, fmt.Errorf("sink: render filename pattern: %w", err)
	}
	if name == "" {
		return Receipt{}, fmt.Errorf("sink: filename pattern produced empty name")
	}
	destDir := filepath.Join(d.root, subdir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return Receipt{}, fmt.Errorf("sink: mkdir %s: %w", destDir, err)
	}
	dest := filepath.Join(destDir, name)

	// Idempotency: if a file already exists at the destination,
	// trust the state DB upstream and don't overwrite. The
	// orchestration loop should have skipped this asset; if we got
	// here anyway, return duplicate so the caller knows.
	if stat, err := os.Stat(dest); err == nil && !stat.IsDir() {
		return Receipt{
			DestPath: dest,
			Status:   "duplicate",
			Size:     stat.Size(),
		}, nil
	}

	src, err := os.Open(in.SourcePath)
	if err != nil {
		return Receipt{}, fmt.Errorf("sink: open source %s: %w", in.SourcePath, err)
	}
	defer src.Close()

	tmp := dest + ".tmp"
	// 0o600: archival photos may carry children's faces and the
	// vendor's metadata. Default to private; the operator can
	// loosen with chmod if they want.
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Receipt{}, fmt.Errorf("sink: open dest tmp %s: %w", tmp, err)
	}
	written, err := io.Copy(out, src)
	if err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return Receipt{}, fmt.Errorf("sink: copy to %s: %w", tmp, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return Receipt{}, fmt.Errorf("sink: fsync %s: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return Receipt{}, fmt.Errorf("sink: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return Receipt{}, fmt.Errorf("sink: rename %s -> %s: %w", tmp, dest, err)
	}

	// EXIF reinjection (atomic via internal tmp+rename, so it
	// rewrites the mtime). Run before the mtime fix-up below.
	// Best-effort: failures are surfaced as a non-fatal error.
	var exifErr error
	if isJPEG(in.Filename) {
		exifErr = exif.Reinject(dest, in.EXIF)
	}

	// Set filesystem mtime from the asset's reported timestamp;
	// archive tools that use mtime then have the right answer
	// even after EXIF reinjection rewrote the file.
	if !in.FileCreatedAt.IsZero() {
		_ = os.Chtimes(dest, time.Now(), in.FileCreatedAt)
	}

	receipt := Receipt{DestPath: dest, Status: "created", Size: written}
	if exifErr != nil {
		return receipt, fmt.Errorf("sink: reinject %s: %w", dest, exifErr)
	}
	return receipt, nil
}

func isJPEG(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return true
	}
	return false
}

// renderPattern expands a pattern string in two passes: strftime
// tokens (%Y, %m, %d, %H, %M, %S, %j) get replaced with the
// FileCreatedAt date components first; then the result is run
// through text/template against PutInput-derived fields.
func renderPattern(pattern string, in PutInput) (string, error) {
	stage1 := expandStrftime(pattern, in.FileCreatedAt)
	tmpl, err := template.New("pattern").Option("missingkey=error").Parse(stage1)
	if err != nil {
		return "", fmt.Errorf("template parse: %w", err)
	}
	ctx := struct {
		Source     string
		ID         string
		FeedItemID string
		Ext        string
	}{
		Source:     in.Source,
		ID:         in.FamlyImageID,
		FeedItemID: in.FeedItemID,
		Ext:        strings.TrimPrefix(filepath.Ext(in.Filename), "."),
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("template execute: %w", err)
	}
	return buf.String(), nil
}

// expandStrftime handles a small subset of strftime tokens. We
// avoid pulling in a strftime library; the supported tokens are
// the ones bairn's templating actually needs.
func expandStrftime(pattern string, when time.Time) string {
	// Use UTC to keep filenames stable across timezones; the
	// timestamp source is UTC anyway.
	when = when.UTC()
	r := strings.NewReplacer(
		"%Y", when.Format("2006"),
		"%m", when.Format("01"),
		"%d", when.Format("02"),
		"%H", when.Format("15"),
		"%M", when.Format("04"),
		"%S", when.Format("05"),
		"%j", when.Format("002"),
	)
	return r.Replace(pattern)
}
