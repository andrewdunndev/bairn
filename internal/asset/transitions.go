package asset

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// Download streams the asset bytes from the signed CDN URL to a
// temporary file. The URL is short-lived; this transition must be
// called soon after the Discovered value was produced.
//
// Streaming-to-disk avoids holding entire video bytes in memory.
// SHA1 is computed during the copy.
func (d Discovered) Download(ctx context.Context, hc *http.Client) (Downloaded, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return Downloaded{}, fmt.Errorf("asset: build download request for %s: %w", d.famlyImageID, err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Downloaded{}, fmt.Errorf("asset: download %s: %w", d.famlyImageID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Downloaded{}, fmt.Errorf("asset: download %s: HTTP %d", d.famlyImageID, resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "bairn-dl-*"+filepath.Ext(d.filename))
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
	}
	receipt, err := immich.Put(ctx, in)
	if err != nil {
		return Uploaded{}, fmt.Errorf("asset: upload %s: %w", s.dl.d.famlyImageID, err)
	}
	return Uploaded{saved: s, immichID: receipt.DestPath, status: receipt.Status}, nil
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
	if err := store.MarkUploaded(ctx, id, u.immichID, u.status, now); err != nil {
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
