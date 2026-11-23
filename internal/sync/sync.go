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
	"gitlab.com/dunn.dev/bairn/internal/albums"
	"gitlab.com/dunn.dev/bairn/internal/asset"
	"gitlab.com/dunn.dev/bairn/internal/retry"
	"gitlab.com/dunn.dev/bairn/internal/sink"
	"gitlab.com/dunn.dev/bairn/internal/state"
	"gitlab.com/dunn.dev/bairn/internal/tags"
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

	// RetryFailed revisits only the state entries that failed (a
	// LastError and no SavedAt) instead of walking the feed for new
	// items. The walk jumps to the newest of their dates and stops past
	// the oldest; Source and IncludeSystemPosts do not apply.
	RetryFailed bool
	// RetryGone also revisits the entries earlier retries marked gone.
	RetryGone bool

	// ProgressEvery logs a progress line every this many pages walked.
	// 0 turns the lines off.
	ProgressEvery int
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

	// DownloadRetry bounds in-run download retries; the zero value is
	// retry.DownloadPolicy.
	DownloadRetry retry.Policy

	// Albums files each uploaded asset into its month and year
	// albums. Optional; nil = no albums. Run flushes it at the end.
	Albums *albums.Filer

	// Tags applies the bairn tag to each uploaded asset. Optional; nil =
	// no tagging. Run flushes it at the end.
	Tags *tags.Tagger

	gate   *uploadGate         // set by Run
	owners map[string][]string // Immich asset ID -> state IDs queued for albums; set by Run
	// tagOwners maps an Immich asset ID to the state IDs queued for the
	// tag; tagMarked writes the outcome back. Set by Run.
	tagOwners map[string][]string
	tagMarked func(ctx context.Context, tagged []string, refused map[string]string, logger *slog.Logger)
	marked    func(ctx context.Context, filed []string, refused map[string]string, logger *slog.Logger)
}

func defaultDownloadRetry() retry.Policy { return retry.DownloadPolicy() }

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
	// Discovered counts every media item read from the feed, filtered
	// or not; Buckets places each in exactly one outcome and sums to it.
	Discovered int     `json:"discovered"`
	Buckets    Buckets `json:"buckets"`
	// Unreconciled is Discovered minus the bucket total; non-zero only
	// if the accounting has a bug (it is logged as an error).
	Unreconciled int `json:"unreconciled,omitempty"`
	Skipped      int `json:"skipped"`
	Saved        int `json:"saved"`
	Uploaded     int `json:"uploaded"`
	Duplicates   int `json:"duplicates"`
	// UploadDuplicates counts Immich uploads the server answered with
	// "duplicate"; they are confirmed, like a fresh upload.
	UploadDuplicates int `json:"uploadDuplicates"`
	// AlbumErrors counts album operations that failed; the assets stay
	// uploaded but unfiled. The CLI exits non-zero while any remain.
	AlbumErrors int `json:"albumErrors,omitempty"`
	// AlbumsRefused counts assets Immich refused for an album (deleted
	// or trashed since upload). They are recorded and not retried, and
	// they do not fail the run.
	AlbumsRefused int `json:"albumsRefused,omitempty"`
	// TagErrors counts tag operations that failed; the assets stay
	// uploaded but untagged, and a rerun tags them. The CLI exits
	// non-zero while any remain.
	TagErrors int `json:"tagErrors,omitempty"`
	// TagsRefused counts assets Immich refused to tag (deleted or
	// trashed since upload). They are recorded and not retried, and they
	// do not fail the run.
	TagsRefused int `json:"tagsRefused,omitempty"`
	// UploadFailed counts assets on disk but not confirmed in Immich.
	// A rerun retries them; the CLI exits non-zero while any remain.
	UploadFailed int `json:"uploadFailed"`
	// ExifErrors counts items saved without their EXIF reinjected. It is
	// warn-only: the item is on disk and uploaded, so it does not fail
	// the run.
	ExifErrors int `json:"exifErrors"`
	// StateErrors counts failures to record progress in the state file
	// (for example ENOSPC). The CLI exits non-zero while any occurred,
	// since a rerun may repeat work or lose album filing.
	StateErrors int `json:"stateErrors,omitempty"`
	Errors      int `json:"errors"`
	// Retry is set on a RetryFailed run.
	Retry *RetrySummary `json:"retry,omitempty"`
	// SystemPostsFiltered counts source-matching images that were
	// skipped because their feed item was system-generated and the
	// run did not pass --include-system-posts. Surfaces the
	// interaction between the system-post default-off rule and the
	// --source filter so operators see what they're missing.
	SystemPostsFiltered int `json:"systemPostsFiltered,omitempty"`
}

// Buckets partitions every discovered media item into exactly one
// terminal outcome of the run. Discovered is the sum; Reconcile checks
// it.
type Buckets struct {
	// SavedNew: fetched and saved to disk this run, with no Immich
	// upload in play (Immich off). A disk-level duplicate counts here.
	SavedNew int `json:"savedNew"`
	// AlreadyDone: recorded as saved (and uploaded, when Immich is on)
	// by an earlier run; nothing left to do.
	AlreadyDone int `json:"alreadyDone"`
	// UploadedNew: confirmed in Immich as newly created this run, whether
	// freshly downloaded or sent from disk.
	UploadedNew int `json:"uploadedNew"`
	// UploadedDuplicate: Immich answered "duplicate", so it already held
	// the file; confirmed all the same.
	UploadedDuplicate int `json:"uploadedDuplicate"`
	// SkippedSystem: media on a system-generated post, without
	// --include-system-posts.
	SkippedSystem int `json:"skippedSystem"`
	// FilteredBySource: excluded by --source (an untagged or unliked
	// image, or any video under a filtered source).
	FilteredBySource int `json:"filteredBySource"`
	// NotReady: a video still transcoding, or with no URL yet; a later
	// run takes it.
	NotReady int `json:"notReady"`
	// WouldProcess: under --dry-run, an item a real run would act on.
	WouldProcess int `json:"wouldProcess"`
	// Failed: could not be fetched, saved, uploaded or recorded this
	// run. A rerun retries it.
	Failed int `json:"failed"`
}

type bucket int

const (
	bSavedNew bucket = iota
	bAlreadyDone
	bUploadedNew
	bUploadedDuplicate
	bSkippedSystem
	bFilteredBySource
	bNotReady
	bWouldProcess
	bFailed
)

func (b *Buckets) add(k bucket) {
	switch k {
	case bSavedNew:
		b.SavedNew++
	case bAlreadyDone:
		b.AlreadyDone++
	case bUploadedNew:
		b.UploadedNew++
	case bUploadedDuplicate:
		b.UploadedDuplicate++
	case bSkippedSystem:
		b.SkippedSystem++
	case bFilteredBySource:
		b.FilteredBySource++
	case bNotReady:
		b.NotReady++
	case bWouldProcess:
		b.WouldProcess++
	case bFailed:
		b.Failed++
	default:
		// An accounting bug, never data: fail loudly in tests.
		panic(fmt.Sprintf("sync: unknown bucket %d", int(k)))
	}
}

// Total is the number of items placed in a bucket.
func (b Buckets) Total() int {
	return b.SavedNew + b.AlreadyDone + b.UploadedNew + b.UploadedDuplicate +
		b.SkippedSystem + b.FilteredBySource + b.NotReady + b.WouldProcess + b.Failed
}

// item records one discovered media item and the bucket it ended in.
func (r *Result) item(k bucket) {
	r.Discovered++
	r.Buckets.add(k)
}

// Reconcile checks that every discovered item landed in exactly one
// bucket, and that the buckets agree with the older counters that are
// bumped on separate paths (so a path that moves one side only shows
// up). A mismatch is a bug in the accounting, not in the data: it is
// logged as an error; a bucket-total mismatch is also recorded in
// Unreconciled.
func (r *Result) Reconcile(logger *slog.Logger) bool {
	ok := true
	if n := r.Buckets.Total(); n != r.Discovered {
		r.Unreconciled = r.Discovered - n
		logger.Error("run summary does not reconcile: buckets do not add up to discovered",
			"discovered", r.Discovered, "bucketTotal", n, "buckets", r.Buckets)
		ok = false
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"uploadedNew vs uploaded", r.Buckets.UploadedNew, r.Uploaded},
		{"uploadedDuplicate vs uploadDuplicates", r.Buckets.UploadedDuplicate, r.UploadDuplicates},
	} {
		if c.got != c.want {
			logger.Error("run summary does not reconcile: counters disagree", "check", c.name, "bucket", c.got, "counter", c.want)
			ok = false
		}
	}
	if r.UploadFailed > r.Buckets.Failed {
		logger.Error("run summary does not reconcile: more upload failures than failed items",
			"uploadFailed", r.UploadFailed, "failed", r.Buckets.Failed)
		ok = false
	}
	return ok
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
	deps.owners = map[string][]string{}
	deps.marked = func(ctx context.Context, filed []string, refused map[string]string, logger *slog.Logger) {
		for immichID, reason := range refused {
			for _, id := range deps.owners[immichID] {
				if err := deps.State.MarkAlbumsRefused(ctx, id, reason); err != nil {
					logger.Error("albums: record refusal", "id", id, "err", err)
					res.StateErrors++
					continue
				}
				res.AlbumsRefused++
				logger.Warn("albums: Immich refused the asset; it will not be filed again", "id", id, "reason", reason)
			}
			delete(deps.owners, immichID)
		}
		for _, immichID := range filed {
			for _, id := range deps.owners[immichID] {
				if err := deps.State.MarkAlbumsFiled(ctx, id, time.Now()); err != nil {
					logger.Error("albums: record filed", "id", id, "err", err)
					res.StateErrors++
				}
			}
			delete(deps.owners, immichID)
		}
	}

	deps.tagOwners = map[string][]string{}
	deps.tagMarked = func(ctx context.Context, tagged []string, refused map[string]string, logger *slog.Logger) {
		markTags(ctx, deps, &res, tagged, refused, logger)
	}

	flushAlbums := func() {
		if deps.Albums == nil {
			return
		}
		// A fresh context: an interrupt must not strand queued adds.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := deps.Albums.Flush(fctx); err != nil {
			res.AlbumErrors++
			logger.Error("albums", "err", err)
		}
		deps.marked(fctx, deps.Albums.TakeFiled(), deps.Albums.TakeRefused(), logger)
	}
	flush := func() {
		flushAlbums()
		flushTags(ctx, deps, &res, logger)
	}

	var plan *retryPlan
	pages := deps.Famly.Pages(ctx)
	if opts.RetryFailed {
		failed := deps.State.Failed()
		if opts.RetryGone {
			failed = append(failed, deps.State.Gone()...)
		} else if n := len(deps.State.Gone()); n > 0 {
			logger.Info("retry-failed: skipping entries marked gone (--retry-gone revisits them)", "gone", n)
		}
		plan = newRetryPlan(failed)
		pages = deps.Famly.PagesFrom(ctx, plan.start)
		logger.Info("retry-failed: revisiting failed entries", "entries", len(plan.targets),
			"undated", plan.undated, "jumpTo", timeOrNone(plan.start), "stopAfter", timeOrNone(plan.floor))
		if len(plan.targets) == 0 {
			res.Retry = plan.summary(ctx, deps.State, opts.DryRun)
			res.FinishedAt = time.Now().UTC()
			return res, nil
		}
	}

	if deps.Albums != nil && !opts.DryRun && plan == nil {
		for _, u := range deps.State.Unfiled() {
			fileInAlbums(ctx, deps, u.ID, u.ImmichAssetID, u.PostedAt, &res, logger)
		}
	}

	if deps.Tags != nil && !opts.DryRun && plan == nil {
		for _, u := range deps.State.Untagged() {
			tagAsset(ctx, deps, u.ID, u.ImmichAssetID, &res, logger)
		}
	}

	walkEnded := true
	for page, err := range pages {
		if err != nil {
			flush()
			res.FinishedAt = time.Now().UTC()
			res.Errors++
			return res, err
		}
		res.PagesWalked++
		if plan != nil {
			plan.notePage(page)
		}
		for _, item := range page.FeedItems {
			if plan != nil {
				processRetryItem(ctx, deps, opts, plan, item, &res, logger)
			} else {
				processItem(ctx, deps, opts, item, &res, logger)
			}
		}
		if opts.ProgressEvery > 0 && res.PagesWalked%opts.ProgressEvery == 0 {
			logProgress(logger, res, page)
		}
		if plan != nil && plan.finished(page) {
			break
		}
		if opts.MaxPages > 0 && res.PagesWalked >= opts.MaxPages {
			logger.Info("max pages reached", "pages", res.PagesWalked, "limit", opts.MaxPages)
			walkEnded = false
			break
		}
	}
	flush()
	res.FinishedAt = time.Now().UTC()
	if plan != nil {
		plan.feedEnded = walkEnded && ctx.Err() == nil
		res.Retry = plan.summary(ctx, deps.State, opts.DryRun)
		logger.Info("retry-failed complete", "failed", res.Retry.Failed, "retried", res.Retry.Retried,
			"fixed", res.Retry.Fixed, "stillFailing", res.Retry.StillFailing, "notFound", res.Retry.NotFound, "gone", res.Retry.Gone)
	}
	res.Reconcile(logger)
	logger.Info("fetch complete",
		"pages", res.PagesWalked,
		"source", string(opts.Source),
		"discovered", res.Discovered, "buckets", res.Buckets, "skipped", res.Skipped,
		"saved", res.Saved, "uploaded", res.Uploaded,
		"duplicates", res.Duplicates, "exifErrors", res.ExifErrors,
		"systemPostsFiltered", res.SystemPostsFiltered,
		"errors", res.Errors,
		"elapsed", res.FinishedAt.Sub(res.StartedAt))
	// Trap D: a filtered source (tagged|liked) that walked pages
	// but matched nothing is the kind of silent zero-result that
	// hides config drift (e.g. childId rotated, no recent likes).
	// Surface it explicitly above the run summary noise.
	if matched := res.Discovered - res.Buckets.FilteredBySource - res.Buckets.SkippedSystem - res.Buckets.NotReady; opts.Source != SourceAll && matched == 0 && res.Discovered > 0 {
		logger.Warn("fetch matched 0 of N discovered images via source filter",
			"source", string(opts.Source),
			"discovered", res.Discovered,
			"hint", "--max-pages caps the walk; if the matching images are deeper in the feed try --max-pages 0, or widen with --source=all to verify the household context resolves")
	}
	return res, nil
}

// logProgress writes the periodic progress line: how far the walk has
// got (pages and the date of the last post read) and the run's tallies.
func logProgress(logger *slog.Logger, res Result, page famly.FeedPage) {
	attrs := []any{"pages", res.PagesWalked}
	if n := len(page.FeedItems); n > 0 {
		attrs = append(attrs, "postDate", page.FeedItems[n-1].CreatedDate.UTC().Format(time.RFC3339))
	}
	logger.Info("progress", append(attrs,
		"discovered", res.Discovered, "saved", res.Saved, "uploaded", res.Uploaded,
		"skipped", res.Skipped, "errors", res.Errors,
		"elapsed", time.Since(res.StartedAt).Round(time.Second))...)
}

// processItem walks one feed item's images and videos, applies the
// source filter, and runs each new asset through the pipeline. Every
// media item lands in exactly one Buckets outcome.
func processItem(ctx context.Context, deps Deps, opts Options, item famly.FeedItem, res *Result, logger *slog.Logger) {
	if item.IsSystemGenerated() && !opts.IncludeSystemPosts {
		// Media on system posts counts toward Skipped so the summary
		// doesn't mislead operators about feed coverage.
		skipped := len(item.Images) + len(item.Videos)
		if skipped > 0 {
			res.Skipped += skipped
			for range skipped {
				res.item(bSkippedSystem)
			}
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
			res.item(bFilteredBySource)
			continue
		}
		res.item(processOne(ctx, deps, opts, asset.DiscoverImage(img, item, opts.Zone), res, logger))
	}
	for _, vid := range item.Videos {
		// Videos carry no child tags or likes, so only --source=all takes
		// them. One still transcoding is left for a later run, which
		// picks it up once Famly serves the final file.
		switch {
		case opts.Source != SourceAll:
			res.Skipped++
			res.item(bFilteredBySource)
		case vid.Transcoding || vid.URL == "":
			res.Skipped++
			res.item(bNotReady)
		default:
			res.item(processOne(ctx, deps, opts, asset.DiscoverVideo(vid, item, opts.Zone), res, logger))
		}
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

// processOne runs the typestate transitions for one asset and returns
// the bucket it ended in.
func processOne(ctx context.Context, deps Deps, opts Options, disc asset.Discovered, res *Result, logger *slog.Logger) bucket {
	id := disc.FamlyImageID()

	// Skip if already saved.
	already, err := deps.State.IsSaved(ctx, id)
	if err != nil {
		logger.Warn("state.IsSaved", "id", id, "err", err)
	}
	if already {
		if !opts.DryRun {
			// Entries saved before FeedItemAt existed gain it here; Discover
			// only fills a zero value and defers its flush.
			if err := deps.State.Discover(ctx, id, state.Asset{FeedItemAt: disc.FeedItemAt()}); err != nil {
				logger.Warn("state.Discover (stamp)", "id", id, "err", err)
			}
		}
		// On disk but maybe not in Immich: a failed upload, an
		// Immich outage, or a save-only run. Upload from the disk sink.
		if deps.Immich != nil {
			if uploaded, _ := deps.State.IsUploaded(ctx, id); !uploaded {
				if opts.DryRun {
					logger.Info("dry-run: would upload saved asset", "id", id)
					return bWouldProcess
				}
				if deps.gate.isStopped() {
					res.UploadFailed++
					return bFailed
				}
				return uploadSaved(ctx, deps, id, res, logger)
			}
		}
		res.Skipped++
		return bAlreadyDone
	}

	if opts.DryRun {
		logger.Info("dry-run: would download+save+upload", "id", id, "source", disc.Source())
		return bWouldProcess
	}

	if err := deps.State.Discover(ctx, id, state.Asset{
		Source:     string(disc.Source()),
		FeedItemID: disc.FeedItemID(),
		FeedItemAt: disc.FeedItemAt(),
	}); err != nil {
		res.Errors++
		logger.Error("discover", "id", id, "err", err)
		return bFailed
	}

	dl, err := download(ctx, deps, disc, logger)
	if err != nil {
		res.Errors++
		logger.Error("download", "id", id, "err", err)
		_ = deps.State.MarkError(ctx, id, err.Error())
		return bFailed
	}
	defer dl.Cleanup()

	saved, err := dl.Save(ctx, deps.Disk, opts.Software)
	if err != nil {
		// Save partial-failure: file may still be on disk. Log,
		// record, and continue to record the partial state.
		res.Errors++
		logger.Error("save", "id", id, "err", err)
		_ = deps.State.MarkError(ctx, id, err.Error())
		return bFailed
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
			return bFailed
		}
		logger.Info("saved", "id", id, "path", saved.FinalPath())
		return bSavedNew
	}

	if deps.gate.isStopped() {
		res.UploadFailed++
		if _, recErr := saved.Record(ctx, deps.State); recErr != nil {
			res.Errors++
			logger.Error("record (saved-only)", "id", id, "err", recErr)
		}
		return bFailed
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
		return bFailed
	}

	if _, err := up.Record(ctx, deps.State); err != nil {
		res.Errors++
		logger.Error("record (uploaded)", "id", id, "err", err)
		return bFailed
	}

	deps.gate.succeeded()
	countUpload(res, up.ImmichStatus())
	fileInAlbums(ctx, deps, id, up.ImmichAssetID(), disc.FileCreatedAt(), res, logger)
	tagAsset(ctx, deps, id, up.ImmichAssetID(), res, logger)
	logger.Info("complete",
		"id", id, "path", saved.FinalPath(),
		"immich_id", up.ImmichAssetID(), "immich_status", up.ImmichStatus())
	return uploadBucket(up.ImmichStatus())
}

// uploadBucket is the outcome of a confirmed Immich upload.
func uploadBucket(status string) bucket {
	if status == "duplicate" {
		return bUploadedDuplicate
	}
	return bUploadedNew
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
func uploadSaved(ctx context.Context, deps Deps, id string, res *Result, logger *slog.Logger) bucket {
	status, err := asset.UploadFromDisk(ctx, deps.Immich, deps.State, deps.Disk.Root(), id)
	if err != nil {
		res.Errors++
		res.UploadFailed++
		logger.Error("upload (retry from disk)", "id", id, "err", err)
		deps.gate.failed(err, logger)
		_ = deps.State.MarkError(ctx, id, err.Error())
		return bFailed
	}
	deps.gate.succeeded()
	countUpload(res, status)
	logger.Info("uploaded from disk", "id", id, "immich_status", status)
	if a, err := deps.State.Get(ctx, id); err == nil {
		if deps.Albums != nil {
			fileInAlbums(ctx, deps, id, a.ImmichAssetID, a.DownloadedAt, res, logger)
		}
		tagAsset(ctx, deps, id, a.ImmichAssetID, res, logger)
	}
	return uploadBucket(status)
}

// fileInAlbums queues an uploaded asset (created or duplicate) for its
// month and year albums. A failure costs the asset its album, never
// its upload.
func fileInAlbums(ctx context.Context, deps Deps, id, immichID string, postedAt time.Time, res *Result, logger *slog.Logger) {
	if deps.Albums == nil {
		return
	}
	if !postedAt.IsZero() {
		if err := deps.State.MarkPosted(ctx, id, postedAt); err != nil {
			logger.Error("albums: record post date", "id", id, "err", err)
			res.StateErrors++
		}
		deps.owners[immichID] = append(deps.owners[immichID], id)
	}
	err := deps.Albums.Add(ctx, immichID, postedAt)
	deps.marked(ctx, deps.Albums.TakeFiled(), deps.Albums.TakeRefused(), logger)
	if err != nil {
		res.AlbumErrors++
		logger.Error("albums", "immich_id", immichID, "err", err)
	}
}
