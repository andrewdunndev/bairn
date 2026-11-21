// Package tags puts one Immich tag on every asset bairn uploads.
//
// The tag is found or created once per run, in the account that owns
// the API key, and assets are added to it in batches. Adding is
// idempotent on the server, so a rerun and a duplicate upload are
// harmless. The package only ever adds: it has no way to untag an asset
// or delete a tag.
package tags

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// DefaultName is the tag bairn applies unless told otherwise.
const DefaultName = "bairn"

// MaxNameLen bounds a tag name.
const MaxNameLen = 100

// flushAt is how many queued assets trigger an early batch add.
const flushAt = 100

// ValidateName checks a configured tag name. A slash would make Immich
// build a tag hierarchy, creating parent tags the key may not cover.
func ValidateName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("tag name is empty; use --no-tag to turn tagging off")
	case len(name) > MaxNameLen:
		return fmt.Errorf("tag name is longer than %d bytes", MaxNameLen)
	case strings.Contains(name, "/"):
		return errors.New("tag name must not contain \"/\" (Immich would treat it as a nested tag)")
	}
	return nil
}

// API is the slice of the Immich client the tagger needs.
type API interface {
	UpsertTag(ctx context.Context, name string) (string, error)
	TagAssets(ctx context.Context, tagID string, assetIDs []string) (refused map[string]string, err error)
}

// Tagger queues assets and adds them to the tag in batches. It is used
// by one goroutine and resolves the tag once, on the first flush.
type Tagger struct {
	api     API
	name    string
	logger  *slog.Logger
	tagID   string
	pending []string
	tagged  []string          // asset IDs added since the last TakeTagged
	refused map[string]string // asset ID -> reason Immich refused it
	// failed is set by a failed flush: Add then stops flushing early, so
	// a key without the tag scopes costs one failed call per run, not one
	// per batch. The final Flush still tries once more.
	failed bool
}

// New returns a Tagger for the tag name. A nil logger uses slog.Default.
func New(api API, name string, logger *slog.Logger) *Tagger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tagger{api: api, name: name, logger: logger, refused: map[string]string{}}
}

// Name is the tag the Tagger applies.
func (t *Tagger) Name() string { return t.name }

// Add queues assetID and flushes when the queue is full. On a flush
// error the assets stay queued for the final Flush.
func (t *Tagger) Add(ctx context.Context, assetID string) error {
	if assetID == "" {
		return errors.New("tags: empty asset id")
	}
	t.pending = append(t.pending, assetID)
	if len(t.pending) >= flushAt && !t.failed {
		return t.Flush(ctx)
	}
	return nil
}

// TakeTagged returns, and forgets, the IDs of assets tagged since the
// last call.
func (t *Tagger) TakeTagged() []string {
	out := t.tagged
	t.tagged = nil
	return out
}

// TakeRefused returns, and forgets, the assets Immich refused, mapped
// to its reason. A refused asset is final: it is never reported as
// tagged and not retried (an asset deleted or trashed in Immich stays
// that way; bairn does not undo it).
func (t *Tagger) TakeRefused() map[string]string {
	out := t.refused
	t.refused = map[string]string{}
	return out
}

// Flush adds every queued asset to the tag.
func (t *Tagger) Flush(ctx context.Context) error {
	if len(t.pending) == 0 {
		return nil
	}
	if t.tagID == "" {
		id, err := t.api.UpsertTag(ctx, t.name)
		if err != nil {
			t.failed = true
			return fmt.Errorf("tag %q: %w", t.name, err)
		}
		t.tagID = id
		t.logger.Info("tag ready", "tag", t.name)
	}
	assets := t.pending
	refused, err := t.api.TagAssets(ctx, t.tagID, assets)
	if err != nil {
		t.failed = true
		return fmt.Errorf("tag %q: %w", t.name, err)
	}
	t.failed = false
	t.pending = nil
	for _, a := range assets {
		if reason, ok := refused[a]; ok {
			t.refused[a] = reason
			continue
		}
		t.tagged = append(t.tagged, a)
	}
	t.logger.Info("tag applied", "tag", t.name, "assets", len(assets), "refused", len(refused))
	return nil
}
