// Package state persists per-asset progress across runs as a single
// JSON file held under an OS-level file lock. Single-writer by
// design; concurrent bairn processes against the same state file
// fail fast with a prescriptive error.
//
// The file lives at $XDG_STATE_HOME/bairn/state.json by default;
// operators can place it next to their save directory instead. See
// ADR 0004.
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Asset is the durable per-image record. JSON-encoded; new fields
// can land with `omitempty` and existing files decode unchanged.
type Asset struct {
	Source        string    `json:"source"`
	FeedItemID    string    `json:"feedItemId,omitempty"`
	DiscoveredAt  time.Time `json:"discoveredAt"`
	DownloadedAt  time.Time `json:"downloadedAt,omitempty"`
	SavedAt       time.Time `json:"savedAt,omitempty"`
	SavedPath     string    `json:"savedPath,omitempty"`
	SHA1          string    `json:"sha1,omitempty"`
	ExifFixedAt   time.Time `json:"exifFixedAt,omitempty"`
	ExifError     string    `json:"exifError,omitempty"`
	UploadedAt    time.Time `json:"uploadedAt,omitempty"`
	ImmichAssetID string    `json:"immichAssetId,omitempty"`
	ImmichStatus  string    `json:"immichStatus,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
	Retries       int       `json:"retries,omitempty"`
}

// Store owns the in-memory map and the file lock. Open it once per
// run; Close it on shutdown.
type Store struct {
	path string
	lock *os.File
	mu   sync.RWMutex
	data map[string]*Asset
}

// ErrNotFound is returned by Get when no asset matches.
var ErrNotFound = errors.New("state: asset not found")

// ErrLocked is returned by Open when another bairn process holds
// the lock on the same state file.
var ErrLocked = errors.New("state: another bairn process holds the lock on this state file")

// Open loads the state file, creating it if absent, and acquires an
// exclusive non-blocking flock for the duration of the Store's life.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("state: create dir for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("state: open %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, fmt.Errorf("state: flock %s: %w", path, err)
	}

	s := &Store{path: path, lock: f, data: map[string]*Asset{}}
	stat, err := f.Stat()
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("state: stat %s: %w", path, err)
	}
	if stat.Size() > 0 {
		if _, err := f.Seek(0, 0); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("state: seek %s: %w", path, err)
		}
		dec := json.NewDecoder(f)
		if err := dec.Decode(&s.data); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("state: decode %s: %w", path, err)
		}
	}
	return s, nil
}

// Close flushes and releases the file lock.
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	flushErr := s.flushLocked()
	unlockErr := unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	closeErr := s.lock.Close()
	s.lock = nil
	return errors.Join(flushErr, unlockErr, closeErr)
}

// flushLocked writes the in-memory map atomically (tmp+fsync+rename).
// fsync the tmp file before rename so the data is durable before
// the directory entry flips. Caller need not hold s.mu; we take
// RLock here.
func (s *Store) flushLocked() error {
	s.mu.RLock()
	buf, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("state: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("state: open tmp: %w", err)
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("state: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("state: fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("state: close tmp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("state: rename: %w", err)
	}
	return nil
}

// Discover inserts an asset record if not already present. Idempotent.
func (s *Store) Discover(ctx context.Context, id string, a Asset) error {
	if id == "" {
		return errors.New("state: Discover with empty id")
	}
	if a.DiscoveredAt.IsZero() {
		a.DiscoveredAt = time.Now().UTC()
	}
	s.mu.Lock()
	if _, exists := s.data[id]; !exists {
		v := a
		s.data[id] = &v
	}
	s.mu.Unlock()
	return s.flushLocked()
}

// MarkDownloaded sets DownloadedAt and SHA1.
func (s *Store) MarkDownloaded(ctx context.Context, id, sha1 string, at time.Time) error {
	return s.update(id, func(a *Asset) {
		a.DownloadedAt = at.UTC()
		a.SHA1 = sha1
	})
}

// MarkSaved sets SavedAt and SavedPath. This is the durability
// commit boundary: a record with SavedAt set means a file exists
// at SavedPath.
func (s *Store) MarkSaved(ctx context.Context, id, savedPath string, at time.Time) error {
	return s.update(id, func(a *Asset) {
		a.SavedAt = at.UTC()
		a.SavedPath = savedPath
	})
}

// MarkExifFixed records that EXIF reinjection succeeded (or failed).
// errMsg empty means success.
func (s *Store) MarkExifFixed(ctx context.Context, id string, at time.Time, errMsg string) error {
	return s.update(id, func(a *Asset) {
		a.ExifFixedAt = at.UTC()
		a.ExifError = errMsg
	})
}

// MarkUploaded sets the Immich receipt fields.
func (s *Store) MarkUploaded(ctx context.Context, id, immichID, status string, at time.Time) error {
	return s.update(id, func(a *Asset) {
		a.UploadedAt = at.UTC()
		a.ImmichAssetID = immichID
		a.ImmichStatus = status
	})
}

// MarkError increments retries and records the message.
func (s *Store) MarkError(ctx context.Context, id, errMsg string) error {
	return s.update(id, func(a *Asset) {
		a.Retries++
		a.LastError = errMsg
	})
}

// update mutates an existing record under the write lock and flushes.
func (s *Store) update(id string, mut func(*Asset)) error {
	s.mu.Lock()
	a, ok := s.data[id]
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	mut(a)
	s.mu.Unlock()
	return s.flushLocked()
}

// Get returns a snapshot of the asset record. The returned Asset
// is a value copy; mutations on it do not affect the store.
func (s *Store) Get(ctx context.Context, id string) (Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.data[id]
	if !ok {
		return Asset{}, ErrNotFound
	}
	return *a, nil
}

// IsSaved is the canonical "skip already-done" check for the fetch
// loop. Returns true iff the asset has SavedAt set.
func (s *Store) IsSaved(ctx context.Context, id string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.data[id]
	if !ok {
		return false, nil
	}
	return !a.SavedAt.IsZero(), nil
}

// IsUploaded reports whether the asset has reached the Immich sink.
func (s *Store) IsUploaded(ctx context.Context, id string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.data[id]
	if !ok {
		return false, nil
	}
	return !a.UploadedAt.IsZero(), nil
}

// Summary aggregates the store into a small report. Used by
// "bairn status".
type Summary struct {
	Total      int       `json:"total"`
	Discovered int       `json:"discovered"`
	Downloaded int       `json:"downloaded"`
	Saved      int       `json:"saved"`
	Uploaded   int       `json:"uploaded"`
	WithErrors int       `json:"withErrors"`
	LastSave   time.Time `json:"lastSave,omitzero"`
	LastUpload time.Time `json:"lastUpload,omitzero"`
}

// Stats returns a Summary. Cheap; an in-memory pass over the map.
func (s *Store) Stats(ctx context.Context) (Summary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sm Summary
	for _, a := range s.data {
		sm.Total++
		switch {
		case !a.UploadedAt.IsZero():
			sm.Uploaded++
		case !a.SavedAt.IsZero():
			sm.Saved++
		case !a.DownloadedAt.IsZero():
			sm.Downloaded++
		default:
			sm.Discovered++
		}
		if a.LastError != "" {
			sm.WithErrors++
		}
		if a.SavedAt.After(sm.LastSave) {
			sm.LastSave = a.SavedAt
		}
		if a.UploadedAt.After(sm.LastUpload) {
			sm.LastUpload = a.UploadedAt
		}
	}
	return sm, nil
}
