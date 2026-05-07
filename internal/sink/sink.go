// Package sink defines the destinations bairn writes assets to.
//
// The Disk sink is the always-on primary; Immich is the optional
// secondary. New sinks (S3, NAS, restic) implement the same
// interface and slot into the orchestration loop without changes
// to the asset transitions.
//
// See ADR 0006.
package sink

import (
	"context"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/exif"
)

// PutInput is the bairn-friendly request shape. Assembled by the
// orchestration loop from the typestate Discovered/Downloaded
// values plus the EXIF Fields derived from the parent feed item.
type PutInput struct {
	// FamlyImageID is the vendor-stable identifier; used for
	// dedup keys, log lines, and metadata.
	FamlyImageID string

	// Source is "feed-image" or "feed-video".
	Source string

	// FeedItemID is the parent post identifier.
	FeedItemID string

	// SourcePath is the path to the bytes on disk. For a Disk
	// sink, this is the temp file that becomes the saved asset.
	// For a chained Immich sink, this is the saved-on-disk path.
	SourcePath string

	// Filename is the intended bare filename including extension,
	// not including the date directory.
	Filename string

	// SHA1 of the file bytes (hex). Sinks may use it for dedup.
	SHA1 string

	// FileCreatedAt is the canonical asset timestamp; Disk sets
	// it as the filesystem mtime, Immich sends it as
	// fileCreatedAt.
	FileCreatedAt time.Time

	// EXIF carries the metadata bairn wants to embed in the
	// saved JPEG. Sinks that don't write EXIF (Immich) ignore.
	EXIF exif.Fields

	// Body is the parent feed item's body text; Disk does nothing
	// with it directly, but it's part of EXIF.UserComment too.
	Body string
}

// Receipt describes what a sink did with a Put.
type Receipt struct {
	// DestPath is the final filesystem path (Disk) or vendor
	// asset id (Immich).
	DestPath string

	// Status is "created" for new uploads/saves, "duplicate" when
	// the sink already had the asset, "skipped" when policy
	// declined the write (e.g. unsafe path).
	Status string

	// Size is the byte count of the asset as the sink saw it.
	Size int64
}

// Sink is the destination interface. Implementations must be safe
// for the orchestration loop to call sequentially per-asset; they
// are not required to be safe for concurrent calls on the same
// asset.
type Sink interface {
	// Put stores the asset and returns a sink-specific receipt.
	// Successful Put implies the artefact is durable for that sink.
	Put(ctx context.Context, in PutInput) (Receipt, error)

	// Name returns a stable identifier ("disk", "immich") used
	// in logs and per-sink retry counters.
	Name() string
}
