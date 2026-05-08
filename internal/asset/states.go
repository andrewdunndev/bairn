// Package asset is the typestate-encoded lifecycle for a single
// piece of media moving from the vendor's surface onto disk and
// (optionally) into Immich.
//
// Phases are distinct Go types: Discovered, Downloaded, Saved,
// Uploaded, Recorded. The only path between them is the
// transition method on the source phase, and each transition
// returns a value of the next phase. Skipping a phase is a
// compile error.
//
// The original ADR 0002 split EXIF reinjection into its own
// ExifFixed phase. In implementation that turned out to fold
// naturally into Save (sink.Disk.Put writes the file and
// reinjects EXIF as one atomic operation; the file does not
// become visible at its final path until both have happened).
// Saved therefore implies "EXIF reinjection attempted"; the state
// DB records whether EXIF actually succeeded.
package asset

import (
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/exif"
)

// Source identifies which vendor surface produced an asset.
type Source string

const (
	SourceFeedImage Source = "feed-image"
	SourceFeedVideo Source = "feed-video"
)

// Discovered is the first phase: an asset known by ID but not yet
// fetched. Holds metadata bairn will need downstream (filename,
// timestamps, caption, sender, per-image tags) plus the
// short-lived signed URL.
type Discovered struct {
	famlyImageID  string
	source        Source
	feedItemID    string
	url           string
	filename      string
	fileCreatedAt time.Time
	tzOffset      string
	body          string
	senderName    string
	tagNames      []string
}

func (d Discovered) FamlyImageID() string  { return d.famlyImageID }
func (d Discovered) Source() Source        { return d.source }
func (d Discovered) FeedItemID() string    { return d.feedItemID }
func (d Discovered) URL() string           { return d.url }
func (d Discovered) Filename() string      { return d.filename }
func (d Discovered) FileCreatedAt() time.Time { return d.fileCreatedAt }
func (d Discovered) Body() string          { return d.body }
func (d Discovered) SenderName() string    { return d.senderName }

// EXIF returns the bundle of metadata bairn knows about this
// asset, ready to embed at Save time.
//
// Body text is sanitized for EXIF (newlines/control chars
// collapsed to spaces) so downstream metadata browsers display
// the description as a single entry rather than splitting on
// embedded newlines. The unsanitized body remains in
// Discovered.body for callers that want it (e.g. a future XMP
// description that supports paragraph structure).
func (d Discovered) EXIF(software string) exif.Fields {
	flatBody := exif.SanitizeText(d.body)
	flatSender := exif.SanitizeText(d.senderName)

	combined := flatBody
	if flatSender != "" {
		if combined != "" {
			combined = combined + " - " + flatSender
		} else {
			combined = flatSender
		}
	}
	offset := d.tzOffset
	if offset == "" {
		offset = "+00:00"
	}
	return exif.Fields{
		DateTimeOriginal:   d.fileCreatedAt,
		OffsetTimeOriginal: offset,
		ImageDescription:   flatBody,
		UserComment:        combined,
		Artist:             flatSender,
		Software:           software,
		// XMP gets the unflattened body; XMP descriptions can
		// safely carry newlines and Unicode without breaking
		// downstream readers (dc:description is a structured
		// alt-language array).
		XMPDescription: d.body,
		XMPKeywords:    d.tagNames,
	}
}

// DiscoverImage constructs a Discovered from a feed image and its
// parent post.
func DiscoverImage(img famly.Image, item famly.FeedItem) Discovered {
	tags := make([]string, 0, len(img.Tags))
	for _, t := range img.Tags {
		if t.Name != "" {
			tags = append(tags, t.Name)
		}
	}
	return Discovered{
		famlyImageID:  img.ImageID,
		source:        SourceFeedImage,
		feedItemID:    item.FeedItemID,
		url:           img.BestURL(),
		filename:      img.ImageID + ".jpg", // initial; Save may rewrite the extension from Content-Type
		fileCreatedAt: pickImageTime(img, item),
		tzOffset:      img.CreatedAt.OffsetString(),
		body:          pickBody(item),
		senderName:    item.SenderName(),
		tagNames:      tags,
	}
}

// DiscoverVideo constructs a Discovered from a feed video and its
// parent post.
func DiscoverVideo(vid famly.Video, item famly.FeedItem) Discovered {
	return Discovered{
		famlyImageID:  vid.VideoID,
		source:        SourceFeedVideo,
		feedItemID:    item.FeedItemID,
		url:           vid.URL,
		filename:      vid.VideoID + ".mp4",
		fileCreatedAt: item.CreatedDate.Time,
		tzOffset:      "+00:00",
		body:          pickBody(item),
		senderName:    item.SenderName(),
	}
}

// Downloaded is the post-fetch phase: the bytes are on disk in a
// temp file. SHA1 is computed. ContentType captures the actual
// mime type as the CDN reported it.
type Downloaded struct {
	d           Discovered
	tempPath    string
	sha1        string
	size        int64
	contentType string
}

func (dl Downloaded) Discovered() Discovered { return dl.d }
func (dl Downloaded) TempPath() string       { return dl.tempPath }
func (dl Downloaded) SHA1() string           { return dl.sha1 }
func (dl Downloaded) Size() int64            { return dl.size }
func (dl Downloaded) ContentType() string    { return dl.contentType }

// Saved is the post-disk phase: the file is at its final path and
// EXIF reinjection has been attempted (success recorded in
// ExifError empty / non-empty).
type Saved struct {
	dl        Downloaded
	finalPath string
	exifError string
	duplicate bool
}

func (s Saved) Downloaded() Downloaded { return s.dl }
func (s Saved) FinalPath() string      { return s.finalPath }
func (s Saved) ExifError() string      { return s.exifError }
func (s Saved) Duplicate() bool        { return s.duplicate }

// Uploaded is the post-Immich phase. Only reachable when the
// Immich sink is configured.
type Uploaded struct {
	saved    Saved
	immichID string
	status   string
}

func (u Uploaded) Saved() Saved          { return u.saved }
func (u Uploaded) ImmichAssetID() string { return u.immichID }
func (u Uploaded) ImmichStatus() string  { return u.status }

// Recorded is terminal. Constructable from Saved (skip-Immich) or
// Uploaded (Immich-enabled).
type Recorded struct {
	saved      Saved
	uploaded   *Uploaded
	recordedAt time.Time
}

func (r Recorded) Saved() Saved             { return r.saved }
func (r Recorded) Uploaded() *Uploaded      { return r.uploaded }
func (r Recorded) RecordedAt() time.Time    { return r.recordedAt }

// pickImageTime selects the most-trusted timestamp for an image:
// the image's own CreatedAt if populated, otherwise the parent
// post's CreatedDate.
func pickImageTime(img famly.Image, item famly.FeedItem) time.Time {
	if !img.CreatedAt.Date.IsZero() {
		return img.CreatedAt.Date.Time
	}
	return item.CreatedDate.Time
}

// pickBody returns the plain-text post body suitable for flat
// EXIF text fields. richTextBody is HTML-formatted and would land
// "<p>...</p>" and "&#34;" artifacts in EXIF; it stays out of
// here. When XMP lands we may consider richTextBody for a
// formatted variant, but XMP-dc:description prefers plain text
// too.
func pickBody(item famly.FeedItem) string {
	return item.Body
}
