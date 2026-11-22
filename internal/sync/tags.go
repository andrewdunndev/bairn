package sync

import (
	"context"
	"log/slog"
	"time"
)

// tagAsset queues an uploaded asset (created or duplicate) for the
// bairn tag. A failure costs the asset its tag, never its upload.
func tagAsset(ctx context.Context, deps Deps, id, immichID string, res *Result, logger *slog.Logger) {
	if deps.Tags == nil || immichID == "" {
		return
	}
	deps.tagOwners[immichID] = append(deps.tagOwners[immichID], id)
	err := deps.Tags.Add(ctx, immichID)
	deps.tagMarked(ctx, deps.Tags.TakeTagged(), deps.Tags.TakeRefused(), logger)
	if err != nil {
		res.TagErrors++
		logger.Error("tags", "immich_id", immichID, "err", err)
	}
}

// markTags writes tagging results to the state file: the tagged assets
// as done, the refused as terminal.
func markTags(ctx context.Context, deps Deps, res *Result, tagged []string, refused map[string]string, logger *slog.Logger) {
	for immichID, reason := range refused {
		for _, id := range deps.tagOwners[immichID] {
			if err := deps.State.MarkTagRefused(ctx, id, reason); err != nil {
				logger.Error("tags: record refusal", "id", id, "err", err)
				res.StateErrors++
				continue
			}
			res.TagsRefused++
			logger.Warn("tags: Immich refused the asset; it will not be tagged again", "id", id, "reason", reason)
		}
		delete(deps.tagOwners, immichID)
	}
	for _, immichID := range tagged {
		for _, id := range deps.tagOwners[immichID] {
			if err := deps.State.MarkTagged(ctx, id, time.Now()); err != nil {
				logger.Error("tags: record tagged", "id", id, "err", err)
				res.StateErrors++
			}
		}
		delete(deps.tagOwners, immichID)
	}
}

// flushTags adds every queued asset to the tag.
func flushTags(ctx context.Context, deps Deps, res *Result, logger *slog.Logger) {
	if deps.Tags == nil {
		return
	}
	// A fresh context: an interrupt must not strand queued adds.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	if err := deps.Tags.Flush(fctx); err != nil {
		res.TagErrors++
		logger.Error("tags", "err", err)
	}
	deps.tagMarked(fctx, deps.Tags.TakeTagged(), deps.Tags.TakeRefused(), logger)
}
