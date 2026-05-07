package famly

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// FamlyTime decodes Famly's non-standard timestamp format.
// The vendor emits "2026-05-06 19:02:44.000000" (space-separated,
// no T, microseconds, no zone) on REST responses; GraphQL emits
// RFC3339. This type accepts both, plus the empty string (null).
type FamlyTime struct{ time.Time }

// UnmarshalJSON parses Famly's wire format, falling back to RFC3339.
func (t *FamlyTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.000000",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("famly: parse timestamp %q: no known layout matched", s)
}

// MarshalJSON emits RFC3339 for stability across consumers.
func (t FamlyTime) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return []byte(`"` + t.Format(time.RFC3339) + `"`), nil
}

// Me is the response shape of /api/me/me/me. bairn only consumes
// LoginID (caller's own ID, used as the predicate for "liked-by-me"
// in feed filters) and Roles2 (current children with targetId and
// title). Other fields are ignored at decode time.
type Me struct {
	LoginID string         `json:"loginId"`
	Email   string         `json:"email"`
	Roles2  []ChildRoleRef `json:"roles2"`
}

// ChildRoleRef is one of Me.Roles2: a child the caller is enrolled
// against. TargetID is the childId used by per-child endpoints.
type ChildRoleRef struct {
	TargetID   string `json:"targetId"`
	TargetType string `json:"targetType"`
	Title      string `json:"title"`
}

// FeedPage is one page of /api/feed/feed/feed. Pagination via
// FeedItems[len-1].FeedItemID and CreatedDate as next-page params.
type FeedPage struct {
	FeedItems []FeedItem `json:"feedItems"`
}

// FeedItem is a single post in the nursery feed. Many fields exist
// on the wire that bairn ignores; only those used downstream are
// declared here. JSON decoding is permissive (extra fields drop).
type FeedItem struct {
	FeedItemID          string    `json:"feedItemId"`
	OriginatorID        string    `json:"originatorId"`
	CreatedDate         FamlyTime `json:"createdDate"`
	Body                string    `json:"body"`
	RichTextBody        string    `json:"richTextBody,omitempty"`
	Sender              *Sender   `json:"sender,omitempty"`
	Images              []Image   `json:"images"`
	Videos              []Video   `json:"videos"`
	SystemPostTypeClass string    `json:"systemPostTypeClass,omitempty"`
}

// IsSystemGenerated reports whether this feed item was produced by
// Famly's automation rather than authored by a human (check-in
// announcements, sign-out notices, etc.). System posts often carry
// templated text that operators don't want embedded as captions
// in their personal photo archive.
func (f FeedItem) IsSystemGenerated() bool {
	return f.SystemPostTypeClass != ""
}

// SenderName returns the display name of whoever posted the feed
// item, or the empty string if absent. Convenience wrapper that
// avoids nil checks at call sites.
func (f FeedItem) SenderName() string {
	if f.Sender == nil {
		return ""
	}
	return f.Sender.Name
}

// Sender describes who posted a feed item. bairn uses Name
// (typically the educator's display name) for EXIF Artist.
type Sender struct {
	ID          string `json:"id,omitempty"`
	LoginID     string `json:"loginId,omitempty"`
	Name        string `json:"name,omitempty"`
	Subtitle    string `json:"subtitle,omitempty"`
}

// Image is one image attached to a feed item. URL and BigURL are
// signed CDN links with an Expiration timestamp; refetch the feed
// page rather than persisting URLs across runs.
//
// The CDN URL pattern encodes the served size as a path segment
// like ".../1024x768/...". Width and Height are the original
// asset's dimensions; BestURL() rewrites the size segment to those
// to fetch the highest-resolution variant the CDN serves.
type Image struct {
	ImageID    string     `json:"imageId"`
	URL        string     `json:"url"`
	BigURL     string     `json:"url_big"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	CreatedAt  ImageTime  `json:"createdAt"`
	Expiration string     `json:"expiration"`
	Liked      bool       `json:"liked"`
	Likes      []Like     `json:"likes"`
	Tags       []ImageTag `json:"tags"`
}

// sizeSegmentPattern matches the "WIDTHxHEIGHT" path segment in
// Famly's CDN URLs, e.g. "/1024x768/" or "/600x450/".
var sizeSegmentPattern = regexp.MustCompile(`/(\d+)x(\d+)/`)

// BestURL returns the highest-resolution URL available for this
// image, using the reported original dimensions to override the
// served size. Falls back to BigURL or URL when dimensions are
// unknown or the URL pattern does not match.
//
// The vendor's CDN is happy to serve any "WxH" segment up to the
// original; asking for the original-size segment yields the
// largest JPEG the server stored for this asset.
func (i Image) BestURL() string {
	candidate := i.BigURL
	if candidate == "" {
		candidate = i.URL
	}
	if i.Width <= 0 || i.Height <= 0 {
		return candidate
	}
	target := fmt.Sprintf("/%dx%d/", i.Width, i.Height)
	if rewritten := sizeSegmentPattern.ReplaceAllString(candidate, target); rewritten != candidate {
		return rewritten
	}
	return candidate
}

// Video is one video attached to a feed item. Shape derived from
// the first populated capture; refine when a richer sample lands.
type Video struct {
	VideoID    string `json:"videoId"`
	URL        string `json:"url"`
	Thumbnail  string `json:"thumbnail"`
	Duration   int    `json:"duration"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Expiration string `json:"expiration"`
}

// ImageTime is the createdAt object Famly attaches to images. The
// field is structured rather than a plain ISO string in their
// schema; the Date inside is the authoritative timestamp, and
// Timezone is the IANA-shaped name (e.g. "UTC") of the zone the
// vendor recorded the image in.
type ImageTime struct {
	Date     FamlyTime `json:"date"`
	Timezone string    `json:"timezone,omitempty"`
}

// OffsetString returns the EXIF-shaped UTC offset ("+HH:MM" or
// "-HH:MM") corresponding to ImageTime.Timezone evaluated at
// ImageTime.Date. Falls back to "+00:00" when the zone is unknown.
func (it ImageTime) OffsetString() string {
	if it.Timezone == "" || it.Timezone == "UTC" {
		return "+00:00"
	}
	loc, err := time.LoadLocation(it.Timezone)
	if err != nil {
		return "+00:00"
	}
	at := it.Date.Time
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, offsetSeconds := at.In(loc).Zone()
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	hours := offsetSeconds / 3600
	mins := (offsetSeconds % 3600) / 60
	return fmt.Sprintf("%s%02d:%02d", sign, hours, mins)
}

// Like represents a caregiver's like on an image. Used by the
// liked-in-feed filter: bairn matches LoginID against the caller's
// own LoginID (and any household relations) to decide inclusion.
type Like struct {
	LoginID string `json:"loginId"`
}

// ImageTag is a per-image kid-tag. Only sometimes populated; when
// it is, ChildID identifies which kid is in the photo.
type ImageTag struct {
	Tag     string `json:"tag"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	ChildID string `json:"childId"`
}

// Relation describes a caregiver associated with a child. bairn
// extracts only LoginID; other fields contain PII and are ignored.
type Relation struct {
	LoginID    string `json:"loginId"`
	ChildID    string `json:"childId"`
	RelationID string `json:"relationId"`
}
