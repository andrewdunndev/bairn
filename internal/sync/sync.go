// Package sync orchestrates the fetch loop: paginate the vendor
// feed, transition each new asset through the lifecycle, write
// progress to the state store. Pure orchestration; the package
// itself does no I/O of its own beyond the dependencies passed in.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/asset"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// Source selects which feed images to download. Mutually exclusive
// by construction; replaces the v0.3.x trio of FeedAll/FeedTagged/
// FeedLiked booleans (where FeedAll's default-true silently masked
// the other two).
type Source string

const (
	// SourceAll downloads every image and video on the feed.
	SourceAll Source = "all"
	// SourceTagged downloads only images tagged with one of the
	// HouseholdChildren. Videos are skipped (Famly does not surface
	// child tags on videos).
	SourceTagged Source = "tagged"
	// SourceLiked downloads only images liked by one of the
	// HouseholdLogins. Videos are skipped.
	SourceLiked Source = "liked"
)

// Validate reports whether s is a known source value.
func (s Source) Validate() error {
	switch s {
	case SourceAll, SourceTagged, SourceLiked:
		return nil
	}
	return fmt.Errorf("sync: unknown source %q (want all|tagged|liked)", string(s))
}

// Options tunes the fetch run.
type Options struct {
	// MaxPages caps the feed walk. 0 means unlimited.
	MaxPages int

	// DryRun stops short of any actual fetch: assets are
	// enumerated and skip-checked but no file lands on disk.
	DryRun bool

	// Source picks the feed filter. Required.
	Source Source

	// HouseholdLogins is the set of login IDs treated as "us" for
	// SourceLiked.
	HouseholdLogins map[string]struct{}

	// HouseholdChildren is the set of child IDs treated as "ours"
	// for SourceTagged.
	HouseholdChildren map[string]struct{}

	// Software is the value for EXIF Software tag, e.g. "bairn 0.1".
	Software string

	// IncludeSystemPosts opts in to processing feed items Famly
	// generates automatically (check-in announcements, sign-out
	// notices, etc.). Off by default; their templated text often
	// isn't what an operator wants embedded as photo captions.
	IncludeSystemPosts bool
}

// Deps are the wired-in collaborators. Disk is required; Immich
// is optional (nil = save-only mode).
type Deps struct {
	Famly  *famly.Client
	Disk   *sink.Disk
	Immich *sink.Immich // optional; nil = no Immich upload
	State  *state.Store
	Logger *slog.Logger
	HTTP   *http.Client
}

// Result is the JSON-shaped fetch summary.
type Result struct {
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt"`
	PagesWalked int       `json:"pagesWalked"`
	Discovered  int       `json:"discovered"`
	Skipped     int       `json:"skipped"`
	Saved       int       `json:"saved"`
	Uploaded    int       `json:"uploaded"`
	Duplicates  int       `json:"duplicates"`
	ExifErrors  int       `json:"exifErrors"`
	Errors      int       `json:"errors"`
}

// Run performs the fetch loop. Returns the run result and an error
// if the loop terminated abnormally. Per-asset failures are
// recorded in the state DB and counted in Result.Errors but do not
// abort the loop.
func Run(ctx context.Context, deps Deps, opts Options) (Result, error) {
	if err := opts.Source.Validate(); err != nil {
		return Result{}, err
	}
	if deps.Disk == nil {
		return Result{}, errors.New("sync: Disk sink is required")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Software == "" {
		opts.Software = "bairn"
	}

	res := Result{StartedAt: time.Now().UTC()}

	for page, err := range deps.Famly.Pages(ctx) {
		if err != nil {
			res.FinishedAt = time.Now().UTC()
			res.Errors++
			return res, err
		}
		res.PagesWalked++
		for _, item := range page.FeedItems {
			processItem(ctx, deps, opts, item, &res, logger)
		}
		if opts.MaxPages > 0 && res.PagesWalked >= opts.MaxPages {
			logger.Info("max pages reached", "pages", res.PagesWalked, "limit", opts.MaxPages)
			break
		}
	}
	res.FinishedAt = time.Now().UTC()
	logger.Info("fetch complete",
		"pages", res.PagesWalked,
		"discovered", res.Discovered, "skipped", res.Skipped,
		"saved", res.Saved, "uploaded", res.Uploaded,
		"duplicates", res.Duplicates, "exifErrors", res.ExifErrors,
		"errors", res.Errors,
		"elapsed", res.FinishedAt.Sub(res.StartedAt))
	return res, nil
}

// processItem walks one feed item's images and videos, applies the
// source filter, and runs each new asset through the pipeline.
func processItem(ctx context.Context, deps Deps, opts Options, item famly.FeedItem, res *Result, logger *slog.Logger) {
	if item.IsSystemGenerated() && !opts.IncludeSystemPosts {
		// Count any media on system posts toward Skipped so the
		// summary doesn't mislead operators about feed coverage.
		skipped := len(item.Images) + len(item.Videos)
		if skipped > 0 {
			res.Skipped += skipped
			logger.Debug("skipped system-generated post",
				"feedItemId", item.FeedItemID,
				"systemPostTypeClass", item.SystemPostTypeClass,
				"assets", skipped)
		}
		return
	}
	for _, img := range item.Images {
		if !shouldDownloadImage(img, opts) {
			res.Skipped++
			continue
		}
		processOne(ctx, deps, opts, asset.DiscoverImage(img, item), res, logger)
	}
	for _, vid := range item.Videos {
		if opts.Source != SourceAll {
			res.Skipped++
			continue
		}
		processOne(ctx, deps, opts, asset.DiscoverVideo(vid, item), res, logger)
	}
}

// shouldDownloadImage applies the source filter to an image.
func shouldDownloadImage(img famly.Image, opts Options) bool {
	switch opts.Source {
	case SourceAll:
		return true
	case SourceTagged:
		for _, tag := range img.Tags {
			if _, ok := opts.HouseholdChildren[tag.ChildID]; ok {
				return true
			}
		}
		return false
	case SourceLiked:
		for _, like := range img.Likes {
			if _, ok := opts.HouseholdLogins[like.LoginID]; ok {
				return true
			}
		}
		if img.Liked && len(opts.HouseholdLogins) > 0 {
			return true
		}
		return false
	}
	return false
}

// processOne runs the typestate transitions for one asset.
func processOne(ctx context.Context, deps Deps, opts Options, disc asset.Discovered, res *Result, logger *slog.Logger) {
	res.Discovered++
	id := disc.FamlyImageID()

	// Skip if already saved.
	already, err := deps.State.IsSaved(ctx, id)
	if err != nil {
		logger.Warn("state.IsSaved", "id", id, "err", err)
	}
	if already {
		res.Skipped++
		// Idempotent re-upload to Immich if it's configured and
		// state says we haven't uploaded yet. Common case: a
		// previous run saved files but Immich was offline.
		if deps.Immich != nil {
			if uploaded, _ := deps.State.IsUploaded(ctx, id); !uploaded {
				logger.Debug("retrying upload for already-saved asset", "id", id)
				// We don't have a Saved value here without re-walking
				// the typestate; deferred to a future "bairn upload-pending"
				// subcommand.
			}
		}
		return
	}

	if err := deps.State.Discover(ctx, id, state.Asset{
		Source:     string(disc.Source()),
		FeedItemID: disc.FeedItemID(),
	}); err != nil {
		res.Errors++
		logger.Error("discover", "id", id, "err", err)
		return
	}

	if opts.DryRun {
		logger.Info("dry-run: would download+save+upload", "id", id, "source", disc.Source())
		return
	}

	dl, err := disc.Download(ctx, deps.HTTP)
	if err != nil {
		res.Errors++
		logger.Error("download", "id", id, "err", err)
		_ = deps.State.MarkError(ctx, id, err.Error())
		return
	}
	defer dl.Cleanup()

	saved, err := dl.Save(ctx, deps.Disk, opts.Software)
	if err != nil {
		// Save partial-failure: file may still be on disk. Log,
		// record, and continue to record the partial state.
		res.Errors++
		logger.Error("save", "id", id, "err", err)
		_ = deps.State.MarkError(ctx, id, err.Error())
		return
	}
	if saved.ExifError() != "" {
		res.ExifErrors++
		logger.Warn("exif reinjection failed", "id", id, "err", saved.ExifError())
	}
	if saved.Duplicate() {
		res.Duplicates++
	} else {
		res.Saved++
	}

	if deps.Immich == nil {
		if _, err := saved.Record(ctx, deps.State); err != nil {
			res.Errors++
			logger.Error("record (saved-only)", "id", id, "err", err)
			return
		}
		logger.Info("saved", "id", id, "path", saved.FinalPath())
		return
	}

	up, err := saved.Upload(ctx, deps.Immich)
	if err != nil {
		res.Errors++
		logger.Error("upload", "id", id, "err", err)
		// Record what we have (saved without uploaded).
		if _, recErr := saved.Record(ctx, deps.State); recErr != nil {
			logger.Error("record (saved-after-upload-failure)", "id", id, "err", recErr)
		}
		_ = deps.State.MarkError(ctx, id, err.Error())
		return
	}

	if _, err := up.Record(ctx, deps.State); err != nil {
		res.Errors++
		logger.Error("record (uploaded)", "id", id, "err", err)
		return
	}

	if up.ImmichStatus() == "duplicate" {
		// Server-side dedup: not counted as a fresh upload.
	} else {
		res.Uploaded++
	}
	logger.Info("complete",
		"id", id, "path", saved.FinalPath(),
		"immich_id", up.ImmichAssetID(), "immich_status", up.ImmichStatus())
}
