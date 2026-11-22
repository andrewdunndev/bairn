package asset

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/safehttp"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
	"gitlab.com/dunn.dev/bairn/internal/urlerr"
)

// TempPrefix starts the name of an in-flight download file. The leading
// dot keeps it out of every scan of the save dir.
const TempPrefix = ".bairn-dl-"

// Download streams the asset bytes from the signed CDN URL to a
// temporary file. The URL is short-lived; this transition must be
// called soon after the Discovered value was produced.
//
// tmpDir is the save root, so the temp file shares a filesystem with
// the final path (no tmpfs pressure, and Put can rename it into place).
// The name is dot-prefixed with a .part suffix: media scans and the
// missing-state guard ignore it, and it is removed on any failure.
//
// Streaming-to-disk avoids holding entire video bytes in memory.
// SHA1 is computed during the copy.
func (d Discovered) Download(ctx context.Context, hc *http.Client, tmpDir string) (Downloaded, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return Downloaded{}, fmt.Errorf("asset: build download request for %s: %w", d.famlyImageID, urlerr.Redact(err))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Downloaded{}, fmt.Errorf("asset: download %s: %w", d.famlyImageID, urlerr.Redact(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Downloaded{}, fmt.Errorf("asset: download %s: %w", d.famlyImageID, &HTTPStatusError{Status: resp.StatusCode})
	}

	tmp, err := os.CreateTemp(tmpDir, TempPrefix+"*.part")
	if err != nil {
		return Downloaded{}, fmt.Errorf("asset: create temp for %s: %w", d.famlyImageID, err)
	}
	hasher := sha1.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return Downloaded{}, fmt.Errorf("asset: stream body for %s: %w", d.famlyImageID, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return Downloaded{}, fmt.Errorf("asset: fsync temp for %s: %w", d.famlyImageID, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return Downloaded{}, fmt.Errorf("asset: close temp for %s: %w", d.famlyImageID, err)
	}
	return Downloaded{
		d:           d,
		tempPath:    tmp.Name(),
		sha1:        hex.EncodeToString(hasher.Sum(nil)),
		size:        written,
		contentType: resp.Header.Get("Content-Type"),
	}, nil
}

// HTTPStatusError is a non-2xx answer from the media CDN.
type HTTPStatusError struct{ Status int }

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

// IsTransient reports whether a Download error is worth another try:
// a connection reset or drop, a truncated body, a timeout or stalled
// transfer, or a 408, 429 or 5xx answer. Anything else (a 4xx, a disk
// error, a cancelled context) will fail the same way again.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var hs *HTTPStatusError
	if errors.As(err, &hs) {
		return hs.Status == http.StatusRequestTimeout || hs.Status == http.StatusTooManyRequests || hs.Status >= 500
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, safehttp.ErrIdle) ||
		errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	// An HTTP/2 stream reset or a server hang-up surfaces as text only.
	msg := err.Error()
	return strings.Contains(msg, "connection reset") || strings.Contains(msg, "unexpected EOF") ||
		strings.Contains(msg, "stream error")
}

// Cleanup removes the temp file created by Download. Callers
// should defer this once Downloaded has been produced; Save
// renames the temp into place, so post-Save Cleanup is a no-op
// (the file is no longer at TempPath).
func (dl Downloaded) Cleanup() {
	if dl.tempPath != "" {
		_ = os.Remove(dl.tempPath)
	}
}

// Save renames the temp file into the configured Disk sink and
// runs EXIF reinjection. Returns Saved on success; Saved.ExifError
// is non-empty if the file is on disk but EXIF reinjection failed.
//
// The returned Saved is durable: a crash from here on is
// recoverable because the file is at its final path.
func (dl Downloaded) Save(ctx context.Context, disk *sink.Disk, software string) (Saved, error) {
	// Choose the file extension from the actual Content-Type the
	// CDN reported, so a vendor that ever serves HEIC or PNG
	// produces correctly-named files instead of misleading .jpg.
	filename := filenameForContentType(dl.d.filename, dl.contentType)
	in := sink.PutInput{
		FamlyImageID:  dl.d.famlyImageID,
		Source:        string(dl.d.source),
		FeedItemID:    dl.d.feedItemID,
		SourcePath:    dl.tempPath,
		Filename:      filename,
		SHA1:          dl.sha1,
		FileCreatedAt: dl.d.fileCreatedAt,
		EXIF:          dl.d.EXIF(software),
		Body:          dl.d.body,
	}
	sidecar, sideErr := dl.d.Sidecar(software)
	if sideErr != nil {
		return Saved{}, fmt.Errorf("asset: sidecar %s: %w", dl.d.famlyImageID, sideErr)
	}
	in.Sidecar = sidecar
	receipt, putErr := disk.Put(ctx, in)
	// Disk.Put may return a non-fatal error (EXIF failed, file
	// saved). We preserve the file path either way.
	if receipt.DestPath == "" {
		return Saved{}, fmt.Errorf("asset: save %s: %w", dl.d.famlyImageID, putErr)
	}
	saved := Saved{
		dl:        dl,
		finalPath: receipt.DestPath,
		duplicate: receipt.Status == "duplicate",
		sidecar:   sidecar,
	}
	if putErr != nil {
		saved.exifError = putErr.Error()
	}
	return saved, nil
}

// Upload sends the saved file to Immich. Only callable when an
// Immich sink is configured.
func (s Saved) Upload(ctx context.Context, immich *sink.Immich) (Uploaded, error) {
	in := sink.PutInput{
		FamlyImageID:  s.dl.d.famlyImageID,
		Source:        string(s.dl.d.source),
		FeedItemID:    s.dl.d.feedItemID,
		SourcePath:    s.finalPath,
		Filename:      filepath.Base(s.finalPath),
		SHA1:          s.dl.sha1,
		FileCreatedAt: s.dl.d.fileCreatedAt,
		Sidecar:       s.sidecar,
	}
	receipt, err := immich.Put(ctx, in)
	if err != nil {
		return Uploaded{}, fmt.Errorf("asset: upload %s: %w", s.dl.d.famlyImageID, err)
	}
	return Uploaded{saved: s, immichID: receipt.DestPath, status: receipt.Status, sha1: receipt.SHA1}, nil
}

// UploadFromDisk is the recovery path for an asset the state store
// holds as saved but not uploaded (a failed upload, an Immich outage,
// or a --no-immich run). It rebuilds the upload from the disk sink:
// the media file at the recorded path plus, for a video, its .xmp
// sidecar. fileCreatedAt is the value Record stored as DownloadedAt.
// On a confirmed upload (created or duplicate) it records the receipt.
// root is the save directory; a recorded path outside it is refused,
// so a damaged state file cannot send an arbitrary file to Immich.
func UploadFromDisk(ctx context.Context, immich *sink.Immich, store *state.Store, root, id string) (status string, err error) {
	a, err := store.Get(ctx, id)
	if err != nil {
		return "", fmt.Errorf("asset: upload %s: %w", id, err)
	}
	if a.SavedPath == "" {
		return "", fmt.Errorf("asset: upload %s: no saved path in state", id)
	}
	if !sink.Within(root, a.SavedPath) {
		return "", fmt.Errorf("asset: upload %s: saved path is outside the save directory", id)
	}
	var sidecar []byte
	if a.Source == string(SourceFeedVideo) {
		sidecar, err = os.ReadFile(a.SavedPath + ".xmp")
		if err != nil {
			return "", fmt.Errorf("asset: upload %s: read sidecar: %w", id, err)
		}
	}
	receipt, err := immich.Put(ctx, sink.PutInput{
		FamlyImageID:  id,
		Source:        a.Source,
		FeedItemID:    a.FeedItemID,
		SourcePath:    a.SavedPath,
		Filename:      filepath.Base(a.SavedPath),
		SHA1:          a.SHA1,
		FileCreatedAt: a.DownloadedAt,
		Sidecar:       sidecar,
	})
	if err != nil {
		return "", fmt.Errorf("asset: upload %s: %w", id, err)
	}
	if err := store.MarkUploaded(ctx, id, receipt.DestPath, receipt.Status, receipt.SHA1, time.Now()); err != nil {
		return "", fmt.Errorf("asset: record (uploaded) %s: %w", id, err)
	}
	return receipt.Status, nil
}

// Record persists the saved-only state to the state DB. The
// commit boundary for runs without Immich.
func (s Saved) Record(ctx context.Context, store *state.Store) (Recorded, error) {
	now := time.Now().UTC()
	id := s.dl.d.famlyImageID
	if err := store.MarkDownloaded(ctx, id, s.dl.sha1, s.dl.d.fileCreatedAt); err != nil {
		return Recorded{}, fmt.Errorf("asset: record (downloaded) %s: %w", id, err)
	}
	if err := store.MarkSaved(ctx, id, s.finalPath, now); err != nil {
		return Recorded{}, fmt.Errorf("asset: record (saved) %s: %w", id, err)
	}
	if err := store.MarkExifFixed(ctx, id, now, s.exifError); err != nil {
		return Recorded{}, fmt.Errorf("asset: record (exif) %s: %w", id, err)
	}
	return Recorded{saved: s, recordedAt: now}, nil
}

// Record persists the saved+uploaded state.
func (u Uploaded) Record(ctx context.Context, store *state.Store) (Recorded, error) {
	now := time.Now().UTC()
	id := u.saved.dl.d.famlyImageID
	if err := store.MarkDownloaded(ctx, id, u.saved.dl.sha1, u.saved.dl.d.fileCreatedAt); err != nil {
		return Recorded{}, fmt.Errorf("asset: record (downloaded) %s: %w", id, err)
	}
	if err := store.MarkSaved(ctx, id, u.saved.finalPath, now); err != nil {
		return Recorded{}, fmt.Errorf("asset: record (saved) %s: %w", id, err)
	}
	if err := store.MarkExifFixed(ctx, id, now, u.saved.exifError); err != nil {
		return Recorded{}, fmt.Errorf("asset: record (exif) %s: %w", id, err)
	}
	if err := store.MarkUploaded(ctx, id, u.immichID, u.status, u.sha1, now); err != nil {
		return Recorded{}, fmt.Errorf("asset: record (uploaded) %s: %w", id, err)
	}
	return Recorded{saved: u.saved, uploaded: &u, recordedAt: now}, nil
}

// ErrSinkNil is returned by transitions that require a sink that
// wasn't configured.
var ErrSinkNil = errors.New("asset: required sink is nil")

// filenameForContentType swaps the extension on filename to one
// that matches contentType. Falls back to the supplied filename
// unchanged when the type is unknown or empty.
func filenameForContentType(filename, contentType string) string {
	ext := extForContentType(contentType)
	if ext == "" {
		return filename
	}
	if old := pathExt(filename); strings.EqualFold(old, ext) {
		return filename
	}
	return strings.TrimSuffix(filename, pathExt(filename)) + ext
}

func pathExt(name string) string {
	for i := len(name) - 1; i >= 0 && name[i] != '/'; i-- {
		if name[i] == '.' {
			return name[i:]
		}
	}
	return ""
}

func extForContentType(ct string) string {
	if ct == "" {
		return ""
	}
	mime := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/heic", "image/heif":
		return ".heic"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/tiff":
		return ".tiff"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	}
	return ""
}
