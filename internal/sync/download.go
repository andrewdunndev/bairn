package sync

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/asset"
)

// download fetches one asset, retrying transient failures (connection
// reset, truncated body, timeout, 5xx; see asset.IsTransient) under
// deps.DownloadRetry. Famly's media URLs are signed and expire, so
// before each retry the feed item is read again for a fresh URL; when
// that lookup fails the same URL is tried again. A failed attempt has
// already removed its partial temp file.
func download(ctx context.Context, deps Deps, disc asset.Discovered, logger *slog.Logger) (asset.Downloaded, error) {
	policy := deps.DownloadRetry
	if policy.Attempts < 1 {
		policy = defaultDownloadRetry()
	}
	cur := disc
	var dl asset.Downloaded
	err := policy.Run(ctx, func(attempt int) error {
		if attempt > 1 {
			cur = freshURL(ctx, deps, cur, logger)
		}
		var err error
		dl, err = cur.Download(ctx, deps.HTTP, deps.Disk.Root())
		return err
	}, asset.IsTransient, func(attempt int, err error, wait time.Duration) {
		logger.Warn("download failed; retrying", "id", disc.FamlyImageID(),
			"attempt", attempt, "of", policy.Attempts, "wait", wait, "err", err)
	})
	return dl, err
}

// freshURL re-reads d's feed item and returns d with its new signed
// URL, or d unchanged when the item cannot be read again.
func freshURL(ctx context.Context, deps Deps, d asset.Discovered, logger *slog.Logger) asset.Discovered {
	if deps.Famly == nil {
		return d
	}
	item, err := deps.Famly.FindItem(ctx, d.FeedItemID(), d.FeedItemAt())
	if err == nil {
		if fresh, ok := d.WithFreshURL(item); ok {
			return fresh
		}
		err = errors.New("item no longer carries the asset")
	}
	logger.Info("download retry reuses the same URL", "id", d.FamlyImageID(), "reason", err.Error())
	return d
}
