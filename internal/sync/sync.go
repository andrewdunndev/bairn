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
	"gitlab.com/dunn.dev/bairn/api/immich"
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

	// Zone is the fallback zone for the date of a video in a post
	// with no zone of its own, image or video. nil means time.Local.
	Zone *time.Location

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

	gate *uploadGate // set by Run
}

// maxUploadFailures is how many uploads in a row may fail before the
// run stops trying Immich. The disk archive is the backup and a rerun
// uploads from it, so a dead or refusing server should cost seconds,
// not a retry cycle per remaining asset.
const maxUploadFailures = 5

// uploadGate stops Immich uploads for the rest of a run after an
// authorization failure or maxUploadFailures failures in a row.
type uploadGate struct {
	fails   int
	stopped bool
}

func (g *uploadGate) isStopped() bool { return g != nil && g.stopped }

func (g *uploadGate) succeeded() {
	if g != nil {
		g.fails = 0
	}
}

func (g *uploadGate) failed(err error, logger *slog.Logger) {
	if g == nil || g.stopped {
		return
	}
	g.fails++
	if errors.Is(err, immich.ErrUnauthorized) || g.fails >= maxUploadFailures {
		g.stopped = true
		logger.Error("immich uploads stopped for this run; the rest stay on disk and a rerun uploads them",
			"consecutiveFailures", g.fails, "err", err)
	}
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
	// UploadDuplicates counts Immich uploads the server answered with
	// "duplicate"; they are confirmed, like a fresh upload.
	UploadDuplicates int `json:"uploadDuplicates"`
	// UploadFailed counts assets on disk but not confirmed in Immich.
	// A rerun retries them; the CLI exits non-zero while any remain.
	UploadFailed int `json:"uploadFailed"`
	ExifErrors   int `json:"exifErrors"`
	Errors       int `json:"errors"`
	// SystemPostsFiltered counts source-matching images that were
	// skipped because their feed item was system-generated and the
	// run did not pass --include-system-posts. Surfaces the
	// interaction between the system-post default-off rule and the
	// --source filter so operators see what they're missing.
	SystemPostsFiltered int `json:"systemPostsFiltered,omitempty"`
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
	deps.gate = &uploadGate{}

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
		"source", string(opts.Source),
		"discovered", res.Discovered, "skipped", res.Skipped,
		"saved", res.Saved, "uploaded", res.Uploaded,
		"duplicates", res.Duplicates, "exifErrors", res.ExifErrors,
		"systemPostsFiltered", res.SystemPostsFiltered,
		"errors", res.Errors,
		"elapsed", res.FinishedAt.Sub(res.StartedAt))
	// Trap D: a filtered source (tagged|liked) that walked pages
	// but matched nothing is the kind of silent zero-result that
	// hides config drift (e.g. childId rotated, no recent likes).
	// Surface it explicitly above the run summary noise.
	if opts.Source != SourceAll && res.Saved == 0 && res.Discovered > 0 {
		logger.Warn("fetch matched 0 of N discovered images via source filter",
			"source", string(opts.Source),
			"discovered", res.Discovered,
			"hint", "--max-pages caps the walk; if the matching images are deeper in the feed try --max-pages 0, or widen with --source=all to verify the household context resolves")
	}
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
			// Trap C: if the operator is filtering by source, count
			// images that the filter would have matched in this
			// system-skipped post. Surfaces in the run summary so
			// operators see when --include-system-posts would
			// expand the result set.
			matched := 0
			if opts.Source != SourceAll {
				for _, img := range item.Images {
					if shouldDownloadImage(img, opts) {
						matched++
					}
				}
			}
			if matched > 0 {
				res.SystemPostsFiltered += matched
			}
			logger.Debug("skipped system-generated post",
				"feedItemId", item.FeedItemID,
				"systemPostTypeClass", item.SystemPostTypeClass,
				"assets", skipped,
				"source_matched", matched)
		}
		return
	}
	for _, img := range item.Images {
		if !shouldDownloadImage(img, opts) {
			res.Skipped++
			continue
		}
		processOne(ctx, deps, opts, asset.DiscoverImage(img, item, opts.Zone), res, logger)
	}
	for _, vid := range item.Videos {
		// A video still transcoding is left for a later run, which
		// picks it up once Famly serves the final file.
		if opts.Source != SourceAll || vid.Transcoding || vid.URL == "" {
			res.Skipped++
			continue
		}
		processOne(ctx, deps, opts, asset.DiscoverVideo(vid, item, opts.Zone), res, logger)
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
		// On disk but maybe not in Immich: a failed upload, an
		// Immich outage, or a save-only run. Upload from the disk sink.
		if deps.Immich != nil {
			if uploaded, _ := deps.State.IsUploaded(ctx, id); !uploaded {
				if opts.DryRun {
					logger.Info("dry-run: would upload saved asset", "id", id)
				} else if deps.gate.isStopped() {
					res.UploadFailed++
				} else {
					uploadSaved(ctx, deps, id, res, logger)
				}
				return
			}
		}
		res.Skipped++
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

	if deps.gate.isStopped() {
		res.UploadFailed++
		if _, recErr := saved.Record(ctx, deps.State); recErr != nil {
			res.Errors++
			logger.Error("record (saved-only)", "id", id, "err", recErr)
		}
		return
	}

	up, err := saved.Upload(ctx, deps.Immich)
	if err != nil {
		res.Errors++
		res.UploadFailed++
		logger.Error("upload", "id", id, "err", err)
		deps.gate.failed(err, logger)
		// Saved without uploaded: a rerun uploads it from disk.
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

	deps.gate.succeeded()
	countUpload(res, up.ImmichStatus())
	logger.Info("complete",
		"id", id, "path", saved.FinalPath(),
		"immich_id", up.ImmichAssetID(), "immich_status", up.ImmichStatus())
}

// countUpload tallies a confirmed Immich upload.
func countUpload(res *Result, status string) {
	if status == "duplicate" {
		res.UploadDuplicates++
	} else {
		res.Uploaded++
	}
}

// uploadSaved uploads an asset the state store holds as saved but not
// confirmed in Immich, reading it back from the disk sink.
func uploadSaved(ctx context.Context, deps Deps, id string, res *Result, logger *slog.Logger) {
	status, err := asset.UploadFromDisk(ctx, deps.Immich, deps.State, deps.Disk.Root(), id)
	if err != nil {
		res.Errors++
		res.UploadFailed++
		logger.Error("upload (retry from disk)", "id", id, "err", err)
		deps.gate.failed(err, logger)
		_ = deps.State.MarkError(ctx, id, err.Error())
		return
	}
	deps.gate.succeeded()
	countUpload(res, status)
	logger.Info("uploaded from disk", "id", id, "immich_status", status)
}
