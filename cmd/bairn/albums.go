package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gitlab.com/dunn.dev/bairn/internal/albums"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// albumConfig builds the album settings from the fetch flags. Albums
// are off unless on is set (default patterns fill any empty one) or a
// pattern is given.
func albumConfig(on bool, month, year, share string, zone *time.Location) (albums.Config, error) {
	cfg := albums.Config{MonthPattern: month, YearPattern: year, Zone: zone}
	if on {
		if cfg.MonthPattern == "" {
			cfg.MonthPattern = albums.DefaultMonthPattern
		}
		if cfg.YearPattern == "" {
			cfg.YearPattern = albums.DefaultYearPattern
		}
	}
	for _, id := range strings.Split(share, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !uuidRE.MatchString(id) {
			return cfg, fmt.Errorf("--album-share %q is not an Immich user ID (a UUID)", id)
		}
		cfg.ShareUserIDs = append(cfg.ShareUserIDs, id)
	}
	if len(cfg.ShareUserIDs) > 0 && !cfg.Enabled() {
		return cfg, fmt.Errorf("--album-share needs an album: set --albums, --album-month or --album-year")
	}
	return cfg, nil
}

var mediaExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".heic": true, ".gif": true,
	".webp": true, ".mp4": true, ".mov": true, ".m4v": true,
}

// checkStateGuard refuses an upload run whose state file is gone while
// the save dir still holds media: with no record of what was uploaded,
// every file would be treated as new. allow overrides.
func checkStateGuard(statePath, saveDir string, allow bool) error {
	if allow {
		return nil
	}
	_, err := os.Stat(statePath)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot check state file %s: %w; fix the path or permissions, or pass --allow-missing-state", statePath, err)
	}
	found := ""
	_ = filepath.WalkDir(saveDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return filepath.SkipAll
		}
		if !d.IsDir() && mediaExt[strings.ToLower(filepath.Ext(p))] {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return nil
	}
	return fmt.Errorf("state file %s is missing but save dir %s already holds media (e.g. %s); "+
		"uploading now would treat every file as new. Restore the state file (check BAIRN_STATE_PATH), "+
		"or pass --allow-missing-state to upload anyway", statePath, saveDir, found)
}
