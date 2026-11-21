// Package albums files uploaded Immich assets into month and year
// albums named from the post date.
//
// Albums are found by exact name among the albums the API key's owner
// owns, and created (shared to the configured users as editors) only
// when absent, so reruns never make a second album of the same name.
// Adds are idempotent on the server. The package only ever adds: it
// has no way to remove an asset from an album or delete an album.
package albums

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DefaultMonthPattern and DefaultYearPattern render "EDC November
// 2026" and "EDC 2026". They are documented defaults for the flags; an
// empty pattern turns that album off.
const (
	DefaultMonthPattern = "EDC %B %Y"
	DefaultYearPattern  = "EDC %Y"
)

// flushAt is how many queued assets trigger an early batch add.
const flushAt = 100

// API is the slice of the Immich client the filer needs.
type API interface {
	FindOwnedAlbum(ctx context.Context, name string) (string, error)
	CreateAlbum(ctx context.Context, name string, userIDs []string) (string, error)
	AddAssetsToAlbum(ctx context.Context, albumID string, assetIDs []string) error
}

// Config selects the albums. Zone is the zone the post date is read
// in; nil means time.Local.
type Config struct {
	MonthPattern string
	YearPattern  string
	ShareUserIDs []string
	Zone         *time.Location
}

// Enabled reports whether any album pattern is set.
func (c Config) Enabled() bool { return c.MonthPattern != "" || c.YearPattern != "" }

// Filer queues assets per album name and adds them in batches. It is
// used by one goroutine and caches album lookups for its lifetime.
type Filer struct {
	api     API
	cfg     Config
	logger  *slog.Logger
	ids     map[string]string   // album name -> album ID
	pending map[string][]string // album name -> asset IDs
	waiting map[string]int      // asset ID -> album adds still outstanding
	filed   []string            // asset IDs whose every album add succeeded
}

// New returns a Filer. A nil logger uses slog.Default.
func New(api API, cfg Config, logger *slog.Logger) *Filer {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Zone == nil {
		cfg.Zone = time.Local
	}
	return &Filer{api: api, cfg: cfg, logger: logger,
		ids: map[string]string{}, pending: map[string][]string{}, waiting: map[string]int{}}
}

// Names renders the album names for a post date, month first. Empty
// patterns are skipped.
func (f *Filer) Names(at time.Time) []string {
	at = at.In(f.cfg.Zone)
	var out []string
	for _, p := range []string{f.cfg.MonthPattern, f.cfg.YearPattern} {
		if p == "" {
			continue
		}
		if n := Render(p, at); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Add queues assetID for the albums of its post date and flushes any
// album whose queue is full. Errors are returned; the asset stays
// queued for the next flush unless the album call itself failed.
func (f *Filer) Add(ctx context.Context, assetID string, at time.Time) error {
	if assetID == "" {
		return errors.New("albums: empty asset id")
	}
	if at.IsZero() {
		return fmt.Errorf("albums: asset %s has no post date", assetID)
	}
	var errs []error
	names := f.Names(at)
	f.waiting[assetID] += len(names)
	if len(names) == 0 {
		f.filed = append(f.filed, assetID)
	}
	for _, name := range names {
		f.pending[name] = append(f.pending[name], assetID)
		if len(f.pending[name]) >= flushAt {
			if err := f.flushOne(ctx, name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// TakeFiled returns, and forgets, the IDs of assets whose every album
// add has succeeded since the last call.
func (f *Filer) TakeFiled() []string {
	out := f.filed
	f.filed = nil
	return out
}

// Flush adds every queued asset to its album.
func (f *Filer) Flush(ctx context.Context) error {
	var errs []error
	for name := range f.pending {
		if err := f.flushOne(ctx, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (f *Filer) flushOne(ctx context.Context, name string) error {
	assets := f.pending[name]
	if len(assets) == 0 {
		return nil
	}
	id, err := f.resolve(ctx, name)
	if err != nil {
		return fmt.Errorf("album %q: %w", name, err)
	}
	if err := f.api.AddAssetsToAlbum(ctx, id, assets); err != nil {
		return fmt.Errorf("album %q: %w", name, err)
	}
	delete(f.pending, name)
	for _, a := range assets {
		if f.waiting[a]--; f.waiting[a] <= 0 {
			delete(f.waiting, a)
			f.filed = append(f.filed, a)
		}
	}
	f.logger.Info("album updated", "album", name, "assets", len(assets))
	return nil
}

// resolve finds the owned album by exact name or creates it.
func (f *Filer) resolve(ctx context.Context, name string) (string, error) {
	if id, ok := f.ids[name]; ok {
		return id, nil
	}
	id, err := f.api.FindOwnedAlbum(ctx, name)
	if err != nil {
		return "", err
	}
	if id == "" {
		if id, err = f.api.CreateAlbum(ctx, name, f.cfg.ShareUserIDs); err != nil {
			return "", err
		}
		f.logger.Info("album created", "album", name, "sharedWith", len(f.cfg.ShareUserIDs))
	}
	f.ids[name] = id
	return id, nil
}

// Render expands %Y (year), %m (month number), %B (English month name)
// and %b (short month name) in pattern against at.
func Render(pattern string, at time.Time) string {
	return strings.TrimSpace(strings.NewReplacer(
		"%Y", at.Format("2006"),
		"%m", at.Format("01"),
		"%B", at.Format("January"),
		"%b", at.Format("Jan"),
	).Replace(pattern))
}
