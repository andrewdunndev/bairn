package sync

import (
	"context"
	"log/slog"
	"time"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/asset"
	"gitlab.com/dunn.dev/bairn/internal/state"
)

// RetrySummary is the result of a RetryFailed run.
type RetrySummary struct {
	// Failed is how many state entries (a LastError and no SavedAt)
	// the run set out to revisit.
	Failed int `json:"failed"`
	// Retried is how many of them the feed walk found and ran through
	// the pipeline again (with DryRun, how many it would).
	Retried int `json:"retried"`
	// Fixed is how many of those are now saved and, with Immich on,
	// uploaded. Always 0 under DryRun.
	Fixed int `json:"fixed"`
	// UploadFailed is how many were saved this run but failed to upload;
	// they are no longer "failed" (the file is on disk) and the next
	// normal fetch uploads them. Not counted as fixed or still failing.
	UploadFailed int `json:"uploadFailed,omitempty"`
	// StillFailing is every entry not saved: those that failed again
	// plus those the walk never found (NotFound). Under DryRun nothing
	// is run, so only NotFound is known and it is left out here;
	// Failed == Fixed + UploadFailed + StillFailing holds for real runs.
	StillFailing int `json:"stillFailing"`
	// NotFound is how many entries the feed walk did not turn up: the
	// post is gone, the video is still transcoding, or --max-pages
	// stopped the walk first.
	NotFound int `json:"notFound"`
	// Undated is how many entries predate FeedItemAt and so were
	// located from their discovery time instead of their post date.
	Undated int `json:"undated,omitempty"`
	// Gone is how many entries this run marked gone: a walk that
	// covered their date (or reached the feed's end) did not find the
	// post. They are still counted in NotFound and StillFailing; later
	// retries skip them unless --retry-gone is given.
	Gone int `json:"gone,omitempty"`
}

// retryPlan is the set of failed assets a RetryFailed run looks for,
// and where in the feed to look.
type retryPlan struct {
	targets   map[string]state.FailedAsset
	attempted map[string]struct{}
	// start is where the walk jumps to: the newest date any target can
	// sit at, its post date when recorded, else when it was discovered
	// (a post cannot be newer than the run that first saw it).
	start time.Time
	// floor is the oldest post date among the targets; the walk stops
	// once a page ends before it. Zero when any target is undated: with
	// no known post date the walk only stops when every target is found.
	floor   time.Time
	undated int
	// seen holds the targets whose post the walk met, even when the
	// asset could not be run (a video still transcoding).
	seen map[string]struct{}
	// oldest is the creation date of the oldest feed item walked.
	oldest time.Time
	// feedEnded is set when the walk ran out of feed rather than being
	// stopped by --max-pages, cancellation or an error.
	feedEnded bool
}

// timeOrNone formats t for a log line, "none" when it is the zero time.
func timeOrNone(t time.Time) string {
	if t.IsZero() {
		return "none"
	}
	return t.Format(time.RFC3339)
}

func newRetryPlan(failed []state.FailedAsset) *retryPlan {
	p := &retryPlan{targets: map[string]state.FailedAsset{}, attempted: map[string]struct{}{}, seen: map[string]struct{}{}}
	allDated := true
	for _, f := range failed {
		p.targets[f.ID] = f
		at := f.FeedItemAt
		if at.IsZero() {
			allDated = false
			p.undated++
			at = f.DiscoveredAt
		}
		if at.After(p.start) {
			p.start = at
		}
		if !f.FeedItemAt.IsZero() && (p.floor.IsZero() || f.FeedItemAt.Before(p.floor)) {
			p.floor = f.FeedItemAt
		}
	}
	if !allDated {
		p.floor = time.Time{}
	}
	return p
}

// notePage records how far back the walk has read.
func (p *retryPlan) notePage(page famly.FeedPage) {
	if n := len(page.FeedItems); n > 0 {
		p.oldest = page.FeedItems[n-1].CreatedDate.Time
	}
}

// covered reports whether a walk that missed t would have met it: the
// feed ran out, or a walked item is older than t's post date.
func (p *retryPlan) covered(t state.FailedAsset) bool {
	return p.feedEnded || (!t.FeedItemAt.IsZero() && !p.oldest.IsZero() && p.oldest.Before(t.FeedItemAt))
}

func (p *retryPlan) want(id string) bool {
	if _, ok := p.targets[id]; !ok {
		return false
	}
	_, done := p.attempted[id]
	return !done
}

// finished reports whether the walk can stop after page: every target
// is found, or the page reached past the oldest post date.
func (p *retryPlan) finished(page famly.FeedPage) bool {
	if len(p.attempted) == len(p.targets) {
		return true
	}
	if p.floor.IsZero() || len(page.FeedItems) == 0 {
		return false
	}
	return page.FeedItems[len(page.FeedItems)-1].CreatedDate.Before(p.floor)
}

// processRetryItem runs the target assets of item through the usual
// pipeline. Filters are not applied: the entry was accepted by an
// earlier run, and --retry-failed takes no filters of its own.
func processRetryItem(ctx context.Context, deps Deps, opts Options, p *retryPlan, item famly.FeedItem, res *Result, logger *slog.Logger) {
	for _, img := range item.Images {
		if _, ok := p.targets[img.ImageID]; ok {
			p.seen[img.ImageID] = struct{}{}
		}
		if p.want(img.ImageID) {
			p.attempted[img.ImageID] = struct{}{}
			res.item(processOne(ctx, deps, opts, asset.DiscoverImage(img, item, opts.Zone), res, logger))
		}
	}
	for _, vid := range item.Videos {
		if _, ok := p.targets[vid.VideoID]; ok {
			p.seen[vid.VideoID] = struct{}{}
		}
		// Not final yet: leave it failing until Famly serves the file.
		if !p.want(vid.VideoID) || vid.Transcoding || vid.URL == "" {
			continue
		}
		p.attempted[vid.VideoID] = struct{}{}
		res.item(processOne(ctx, deps, opts, asset.DiscoverVideo(vid, item, opts.Zone), res, logger))
	}
}

// summary tallies the run once the walk has ended.
func (p *retryPlan) summary(ctx context.Context, st *state.Store, dryRun bool) *RetrySummary {
	s := &RetrySummary{Failed: len(p.targets), Retried: len(p.attempted), Undated: p.undated}
	s.NotFound = s.Failed - s.Retried
	if dryRun {
		return s
	}
	s.StillFailing = s.NotFound
	for id, t := range p.targets {
		_, seen := p.seen[id]
		switch {
		case seen && !t.GoneAt.IsZero():
			_ = st.ClearGone(ctx, id)
		case !seen && p.covered(t):
			if st.MarkGone(ctx, id, time.Now()) == nil {
				s.Gone++
			}
		}
	}
	for id := range p.attempted {
		a, err := st.Get(ctx, id)
		switch {
		case err == nil && !a.SavedAt.IsZero() && a.LastError != "":
			s.UploadFailed++
		case err == nil && !a.SavedAt.IsZero():
			s.Fixed++
		default:
			s.StillFailing++
		}
	}
	return s
}
