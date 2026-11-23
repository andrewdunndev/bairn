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
	"sort"
	"sync"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/privdir"
	"golang.org/x/sys/unix"
)

// Asset is the durable per-image record. JSON-encoded; new fields
// can land with `omitempty` and existing files decode unchanged.
type Asset struct {
	Source     string `json:"source"`
	FeedItemID string `json:"feedItemId,omitempty"`
	// FeedItemAt is the creation date of the feed item the asset came
	// from, the key the feed is ordered by. It lets a retry jump the
	// feed to the item; entries from before v0.7.0 lack it.
	FeedItemAt   time.Time `json:"feedItemAt,omitempty"`
	DiscoveredAt time.Time `json:"discoveredAt"`
	DownloadedAt time.Time `json:"downloadedAt,omitempty"`
	SavedAt      time.Time `json:"savedAt,omitempty"`
	SavedPath    string    `json:"savedPath,omitempty"`
	SHA1         string    `json:"sha1,omitempty"`
	// UploadSHA1 is the SHA-1 of the bytes handed to Immich, which is
	// Immich's asset checksum. It differs from SHA1 (taken at download,
	// before EXIF reinjection) for stamped items. Entries uploaded
	// before it existed lack it.
	UploadSHA1    string    `json:"uploadSha1,omitempty"`
	ExifFixedAt   time.Time `json:"exifFixedAt,omitempty"`
	ExifError     string    `json:"exifError,omitempty"`
	UploadedAt    time.Time `json:"uploadedAt,omitempty"`
	ImmichAssetID string    `json:"immichAssetId,omitempty"`
	ImmichStatus  string    `json:"immichStatus,omitempty"`
	PostedAt      time.Time `json:"postedAt,omitempty"`
	AlbumsFiledAt time.Time `json:"albumsFiledAt,omitempty"`
	// AlbumsRefused holds Immich's reason when it refused to add the
	// asset to an album (an asset deleted or trashed since upload).
	// It is terminal: the asset is never queued for albums again.
	AlbumsRefused string `json:"albumsRefused,omitempty"`
	// TaggedAt marks the asset as carrying the Immich tag. Entries from
	// before tagging existed lack it and are tagged once, from state.
	TaggedAt time.Time `json:"taggedAt,omitempty"`
	// TagRefused holds Immich's reason when it refused to tag the asset
	// (deleted or trashed since upload). Terminal, like AlbumsRefused.
	TagRefused string `json:"tagRefused,omitempty"`
	LastError  string `json:"lastError,omitempty"`
	// GoneAt is when a --retry-failed walk that covered the entry's
	// post date (or reached the feed's end) did not find the post.
	// Failed skips such entries; --retry-gone revisits them.
	GoneAt  time.Time `json:"goneAt,omitzero"`
	Retries int       `json:"retries,omitempty"`
	// ImmichVerifiedAt is when Immich last confirmed it holds this
	// entry's UploadSHA1 under ImmichAssetID, not trashed. Cleared when
	// a later check no longer confirms it.
	ImmichVerifiedAt time.Time `json:"immichVerifiedAt,omitzero"`
	// VerifyResult is the last failed verify outcome (trashed, mismatch,
	// absent); empty when verified or never checked.
	VerifyResult string `json:"verifyResult,omitempty"`
	// PrunedAt is the tombstone left when the local file was deleted
	// after verification. Every other field is kept, so no path
	// downloads or uploads the entry again; SavedFiles and Requeue skip
	// it so a deleted file is never read as a missing one.
	PrunedAt time.Time `json:"prunedAt,omitzero"`
}

// Store owns the in-memory map and the file lock. Open it once per
// run; Close it on shutdown.
type Store struct {
	path string
	lock *os.File
	mu   sync.Mutex
	data map[string]*Asset

	// dirty counts changes not yet written; the file is rewritten
	// once it reaches flushEvery, and always on Flush and Close.
	dirty      int
	flushEvery int
}

// DefaultFlushEvery is how many changes accumulate before the state
// file is rewritten. A crash loses at most this many changes; the
// next run redoes them, and the disk and Immich checksum dedupe
// absorb the repeats. Rewriting the whole file per change made a
// full-history run quadratic.
const DefaultFlushEvery = 50

// ErrNotFound is returned by Get when no asset matches.
var ErrNotFound = errors.New("state: asset not found")

// ErrLocked is returned by Open when another bairn process holds
// the lock on the same state file.
var ErrLocked = errors.New("state: another bairn process holds the lock on this state file")

// Open loads the state file, creating it if absent, and acquires an
// exclusive non-blocking flock for the duration of the Store's life.
// The lock is on a sibling <path>.lock that is never renamed: the
// state file itself is replaced on every flush, which would leave a
// lock on it pointing at an unlinked inode.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("state: create dir for %s: %w", path, err)
	}
	privdir.WarnLoose(filepath.Dir(path))
	f, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("state: open lock for %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, fmt.Errorf("state: flock %s: %w", path, err)
	}

	s := &Store{path: path, lock: f, data: map[string]*Asset{}, flushEvery: DefaultFlushEvery}
	buf, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = s.Close()
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}
	if len(buf) > 0 {
		if err := json.Unmarshal(buf, &s.data); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("state: decode %s: %w", path, err)
		}
	}
	return s, nil
}

// Close flushes any unwritten changes and releases the file lock.
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	flushErr := s.Flush()
	unlockErr := unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
	closeErr := s.lock.Close()
	s.lock = nil
	return errors.Join(flushErr, unlockErr, closeErr)
}

// Flush writes any unwritten changes to disk. A no-op when clean.
func (s *Store) Flush() error {
	s.mu.Lock()
	if s.dirty == 0 {
		s.mu.Unlock()
		return nil
	}
	err := s.writeLocked()
	s.mu.Unlock()
	return err
}

// changed records one mutation and flushes when the batch is full.
// Caller holds s.mu.
func (s *Store) changedLocked() error {
	s.dirty++
	if s.dirty >= s.flushEvery {
		return s.writeLocked()
	}
	return nil
}

// SetFlushEvery sets how many changes accumulate before a write; n < 1
// writes on every change.
func (s *Store) SetFlushEvery(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushEvery = max(n, 1)
}

// writeLocked writes the in-memory map atomically (tmp+fsync+rename).
// fsync the tmp file before rename so the data is durable before
// the directory entry flips. Caller holds s.mu.
func (s *Store) writeLocked() error {
	buf, err := json.MarshalIndent(s.data, "", "  ")
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
	s.dirty = 0
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
	defer s.mu.Unlock()
	if cur, exists := s.data[id]; exists {
		// Entries written before FeedItemAt existed gain it here. The
		// stamp is non-critical (re-derived from the feed on any later
		// run), so it only marks the store dirty: it rides the next
		// critical write or Close instead of rewriting the file itself.
		s.stampFeedItemAtLocked(cur, a.FeedItemAt)
		return nil
	}
	a.FeedItemAt = a.FeedItemAt.UTC()
	v := a
	s.data[id] = &v
	return s.changedLocked()
}

// stampFeedItemAtLocked fills a zero FeedItemAt and reports whether it
// did. The caller holds s.mu.
func (s *Store) stampFeedItemAtLocked(cur *Asset, at time.Time) bool {
	if !cur.FeedItemAt.IsZero() || at.IsZero() {
		return false
	}
	cur.FeedItemAt = at.UTC()
	s.dirty++
	return true
}

// StampFeedItemAt fills a zero FeedItemAt on an existing entry and
// reports whether it wrote one. Like Discover's stamp it only marks the
// store dirty.
func (s *Store) StampFeedItemAt(id string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.data[id]
	return ok && s.stampFeedItemAtLocked(cur, at)
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
		a.LastError = "" // a failed fetch is resolved once the file is on disk
		a.GoneAt = time.Time{}
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

// ErrImmichIDConflict is returned by MarkUploaded when the entry
// already holds a different Immich asset id. The stored id is kept.
var ErrImmichIDConflict = errors.New("state: entry already has a different Immich asset id")

// MarkUploaded sets the Immich receipt fields and the SHA-1 of the
// bytes sent, in one update. It never replaces a non-empty
// ImmichAssetID with a different one: that would orphan the first
// Immich asset. Such a call changes nothing and returns
// ErrImmichIDConflict.
func (s *Store) MarkUploaded(ctx context.Context, id, immichID, status, uploadSHA1 string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data[id]
	if !ok {
		return ErrNotFound
	}
	if a.ImmichAssetID != "" && a.ImmichAssetID != immichID {
		return fmt.Errorf("%w: %s has %s, got %s", ErrImmichIDConflict, id, a.ImmichAssetID, immichID)
	}
	a.UploadedAt = at.UTC()
	a.ImmichAssetID = immichID
	a.ImmichStatus = status
	a.UploadSHA1 = uploadSHA1
	return s.changedLocked()
}

// MarkPosted records the post date an upload is to be filed under.
func (s *Store) MarkPosted(ctx context.Context, id string, at time.Time) error {
	return s.update(id, func(a *Asset) { a.PostedAt = at.UTC() })
}

// MarkAlbumsFiled records that every album add for the asset succeeded.
func (s *Store) MarkAlbumsFiled(ctx context.Context, id string, at time.Time) error {
	return s.update(id, func(a *Asset) { a.AlbumsFiledAt = at.UTC() })
}

// MarkAlbumsRefused records that Immich refused the asset for an
// album, with its reason. The asset is not queued for albums again.
func (s *Store) MarkAlbumsRefused(ctx context.Context, id, reason string) error {
	return s.update(id, func(a *Asset) { a.AlbumsRefused = reason })
}

// MarkTagged records that the asset carries the Immich tag.
func (s *Store) MarkTagged(ctx context.Context, id string, at time.Time) error {
	return s.update(id, func(a *Asset) { a.TaggedAt = at.UTC() })
}

// MarkTagRefused records that Immich refused to tag the asset, with its
// reason. The asset is not queued for tagging again.
func (s *Store) MarkTagRefused(ctx context.Context, id, reason string) error {
	return s.update(id, func(a *Asset) { a.TagRefused = reason })
}

// owedTag reports whether an asset is owed the tag: uploaded, with an
// Immich asset ID, not yet tagged and not refused.
func owedTag(a *Asset) bool {
	return !a.UploadedAt.IsZero() && a.ImmichAssetID != "" && a.TaggedAt.IsZero() && a.TagRefused == ""
}

// UntaggedAsset is an uploaded asset still owed its tag.
type UntaggedAsset struct {
	ID            string
	ImmichAssetID string
}

// Untagged lists the assets owed the tag (see owedTag), ordered by ID.
// Tagging writes TaggedAt, so an entry from before tagging existed is
// listed once and then never again.
func (s *Store) Untagged() []UntaggedAsset {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UntaggedAsset
	for id, a := range s.data {
		if owedTag(a) {
			out = append(out, UntaggedAsset{ID: id, ImmichAssetID: a.ImmichAssetID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// UploadSHA1Candidate is an uploaded asset whose UploadSHA1 is blank.
type UploadSHA1Candidate struct {
	ID          string
	SavedPath   string
	UploadedAt  time.Time
	ExifFixedAt time.Time
}

// WithoutUploadSHA1 lists the uploaded assets (UploadedAt set) whose
// UploadSHA1 is blank, ordered by ID.
func (s *Store) WithoutUploadSHA1() []UploadSHA1Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UploadSHA1Candidate
	for id, a := range s.data {
		if !a.UploadedAt.IsZero() && a.UploadSHA1 == "" {
			out = append(out, UploadSHA1Candidate{ID: id, SavedPath: a.SavedPath, UploadedAt: a.UploadedAt, ExifFixedAt: a.ExifFixedAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// StampUploadSHA1 sets a blank UploadSHA1 and reports whether it did;
// a value already there is never overwritten. Like Discover's stamp it
// only marks the store dirty, so a bulk backfill rides one Flush.
func (s *Store) StampUploadSHA1(id, sha1 string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data[id]
	if !ok || a.UploadSHA1 != "" || sha1 == "" {
		return false
	}
	a.UploadSHA1 = sha1
	s.dirty++
	return true
}

// UnfiledAsset is an uploaded asset still owed its albums.
type UnfiledAsset struct {
	ID            string
	ImmichAssetID string
	PostedAt      time.Time
}

// owedAlbums returns the date an asset is to be filed under and
// whether it is owed albums: uploaded, with an Immich asset ID, not
// yet filed and not refused. Entries written before PostedAt existed
// fall back to DownloadedAt, which has always held the item's
// fileCreatedAt, the same value the live path records as PostedAt.
func owedAlbums(a *Asset) (time.Time, bool) {
	if a.UploadedAt.IsZero() || a.ImmichAssetID == "" || !a.AlbumsFiledAt.IsZero() || a.AlbumsRefused != "" {
		return time.Time{}, false
	}
	at := a.PostedAt
	if at.IsZero() {
		at = a.DownloadedAt
	}
	return at, !at.IsZero()
}

// Unfiled lists assets owed their albums (see owedAlbums), ordered by
// ID. Filing writes PostedAt and AlbumsFiledAt, so an entry from before
// filing was tracked is listed once and then never again.
func (s *Store) Unfiled() []UnfiledAsset {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UnfiledAsset
	for id, a := range s.data {
		if at, ok := owedAlbums(a); ok {
			out = append(out, UnfiledAsset{ID: id, ImmichAssetID: a.ImmichAssetID, PostedAt: at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// FailedAsset is an entry whose fetch failed and that never reached
// the disk.
type FailedAsset struct {
	ID         string
	Source     string
	FeedItemID string
	// FeedItemAt is zero for an entry written before it was recorded.
	FeedItemAt   time.Time
	DiscoveredAt time.Time
	// GoneAt is set on entries Gone lists.
	GoneAt time.Time
}

// Failed lists the entries with a LastError and no SavedAt, ordered by
// ID, that are not marked gone: the work a retry-only run revisits. An
// asset saved but not uploaded is not listed; an ordinary run uploads
// those from disk.
func (s *Store) Failed() []FailedAsset { return s.failed(false) }

// Gone lists the failed entries marked gone, ordered by ID.
func (s *Store) Gone() []FailedAsset { return s.failed(true) }

func (s *Store) failed(gone bool) []FailedAsset {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []FailedAsset
	for id, a := range s.data {
		if a.LastError != "" && a.SavedAt.IsZero() && !a.GoneAt.IsZero() == gone {
			out = append(out, FailedAsset{ID: id, Source: a.Source, FeedItemID: a.FeedItemID,
				FeedItemAt: a.FeedItemAt, DiscoveredAt: a.DiscoveredAt, GoneAt: a.GoneAt})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MarkGone records that a covering feed walk did not find the entry's
// post, so retry runs skip it.
func (s *Store) MarkGone(ctx context.Context, id string, at time.Time) error {
	return s.update(id, func(a *Asset) { a.GoneAt = at.UTC() })
}

// ClearGone removes the gone mark.
func (s *Store) ClearGone(ctx context.Context, id string) error {
	return s.update(id, func(a *Asset) { a.GoneAt = time.Time{} })
}

// MarkError increments retries and records the message.
func (s *Store) MarkError(ctx context.Context, id, errMsg string) error {
	return s.update(id, func(a *Asset) {
		a.Retries++
		a.LastError = errMsg
		a.GoneAt = time.Time{}
	})
}

// Requeue marks the saved entries ids as failed in one update: SavedAt
// and SavedPath are cleared and LastError set, so Failed lists them and
// IsSaved is false. Every other field, the Immich receipt included, is
// kept. Unknown, unsaved and pruned ids are skipped. The count of entries
// changed is returned; the write is deferred to Flush or Close.
func (s *Store) Requeue(ids []string, errMsg string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, id := range ids {
		a, ok := s.data[id]
		if !ok || a.SavedAt.IsZero() || !a.PrunedAt.IsZero() {
			continue
		}
		a.SavedAt = time.Time{}
		a.SavedPath = ""
		a.LastError = errMsg
		a.GoneAt = time.Time{}
		n++
	}
	if n > 0 {
		s.dirty += n
	}
	return n
}

// update mutates an existing record under the lock.
func (s *Store) update(id string, mut func(*Asset)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data[id]
	if !ok {
		return ErrNotFound
	}
	mut(a)
	return s.changedLocked()
}

// Get returns a snapshot of the asset record. The returned Asset
// is a value copy; mutations on it do not affect the store.
func (s *Store) Get(ctx context.Context, id string) (Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data[id]
	if !ok {
		return Asset{}, ErrNotFound
	}
	return *a, nil
}

// IsSaved is the canonical "skip already-done" check for the fetch
// loop. Returns true iff the asset has SavedAt set.
func (s *Store) IsSaved(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data[id]
	if !ok {
		return false, nil
	}
	return !a.SavedAt.IsZero(), nil
}

// IsUploaded reports whether the asset has reached the Immich sink.
func (s *Store) IsUploaded(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data[id]
	if !ok {
		return false, nil
	}
	return !a.UploadedAt.IsZero(), nil
}

// SavedFile is a saved entry (SavedAt set) and the path it recorded.
type SavedFile struct {
	ID        string
	SavedPath string
}

// SavedFiles lists the entries with SavedAt set and no PrunedAt,
// ordered by ID: the entries whose file should be on disk.
func (s *Store) SavedFiles() []SavedFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SavedFile
	for id, a := range s.data {
		if !a.SavedAt.IsZero() && a.PrunedAt.IsZero() {
			out = append(out, SavedFile{ID: id, SavedPath: a.SavedPath})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Summary aggregates the store into a small report. Used by
// "bairn status".
type Summary struct {
	Total      int `json:"total"`
	Discovered int `json:"discovered"`
	Downloaded int `json:"downloaded"`
	Saved      int `json:"saved"`
	Uploaded   int `json:"uploaded"`
	WithErrors int `json:"withErrors"`
	// Undated counts saved entries with no feedItemAt stamp.
	Undated int `json:"undated"`
	// AlbumsFiled, AlbumsUnfiled and AlbumsRefused count uploads by
	// album state; Unfiled is what a run with albums on still owes.
	AlbumsFiled   int `json:"albumsFiled"`
	AlbumsUnfiled int `json:"albumsUnfiled"`
	AlbumsRefused int `json:"albumsRefused"`
	// TagsApplied, TagsOwed and TagsRefused count uploads by tag state;
	// Owed is what a run with tagging on still owes.
	TagsApplied int `json:"tagsApplied"`
	TagsOwed    int `json:"tagsOwed"`
	TagsRefused int `json:"tagsRefused"`
	// Verified counts entries Immich last confirmed; VerifyFailed those
	// whose last check failed (trashed, mismatch, absent); Pruned those
	// whose local file was deleted.
	Verified     int       `json:"verified"`
	VerifyFailed int       `json:"verifyFailed"`
	Pruned       int       `json:"pruned"`
	LastSave     time.Time `json:"lastSave,omitzero"`
	LastUpload   time.Time `json:"lastUpload,omitzero"`
}

// Stats returns a Summary. Cheap; an in-memory pass over the map.
func (s *Store) Stats(ctx context.Context) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		switch {
		case !a.AlbumsFiledAt.IsZero():
			sm.AlbumsFiled++
		case a.AlbumsRefused != "":
			sm.AlbumsRefused++
		default:
			if _, ok := owedAlbums(a); ok {
				sm.AlbumsUnfiled++
			}
		}
		switch {
		case !a.TaggedAt.IsZero():
			sm.TagsApplied++
		case a.TagRefused != "":
			sm.TagsRefused++
		case owedTag(a):
			sm.TagsOwed++
		}
		if !a.SavedAt.IsZero() && a.FeedItemAt.IsZero() {
			sm.Undated++
		}
		if !a.ImmichVerifiedAt.IsZero() {
			sm.Verified++
		}
		if a.VerifyResult != "" {
			sm.VerifyFailed++
		}
		if !a.PrunedAt.IsZero() {
			sm.Pruned++
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
