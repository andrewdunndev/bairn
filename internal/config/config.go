// Package config loads bairn's configuration from env vars; fetch
// flags override them. bairn reads no configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/privdir"
	"gitlab.com/dunn.dev/bairn/internal/session"
)

// DefaultProgressEvery is the page interval between progress lines.
const DefaultProgressEvery = 10

// Config is the application-level configuration. Populated by Load.
type Config struct {
	// FamlyAccessToken is a pasted session token, for accounts where
	// email and password cannot be used (MFA). It does not refresh.
	FamlyAccessToken string

	// FamlyEmail and FamlyPassword enable token refresh on 401.
	FamlyEmail    string
	FamlyPassword string

	// FamlyDeviceID (FAMLY_DEVICE_ID) pins the device identifier sent to
	// Famly's Authenticate mutation, for CI and containers. Optional:
	// when unset, the id stored in the session file is used, and a
	// random UUID is generated and stored on first use.
	FamlyDeviceID string

	// FamlyBaseURL overrides the production endpoint. Read from
	// FAMLY_BASE_URL for tests only; it is not an operator setting.
	FamlyBaseURL string

	// ImmichBaseURL is the Immich API root, e.g.
	// "https://immich.home.example/api".
	ImmichBaseURL string

	// ImmichAPIKey is the x-api-key header value.
	ImmichAPIKey string

	// Albums turns on Immich albums with the default name patterns
	// (BAIRN_ALBUMS=1). AlbumMonth and AlbumYear (BAIRN_ALBUM_MONTH,
	// BAIRN_ALBUM_YEAR) set a pattern and enable that album on their
	// own; AlbumShare (BAIRN_ALBUM_SHARE) is a comma-separated list of
	// Immich user IDs a new album is shared with as editors.
	Albums     bool
	AlbumMonth string
	AlbumYear  string
	AlbumShare string

	// ProgressEvery (BAIRN_PROGRESS_EVERY) is how many feed pages pass
	// between progress log lines; 0 turns them off. Default 10.
	ProgressEvery int

	// SaveDir is the root for saved photos and videos.
	// Default: $XDG_DATA_HOME/bairn/assets
	SaveDir string

	// SessionPath is the Famly session file (device id and last
	// access token), always next to the state file.
	SessionPath string

	// StatePath is the JSON state file path.
	// Default: $XDG_STATE_HOME/bairn/state.json
	StatePath string

	// Zone is the IANA name of the fallback zone for videos in posts
	// with no zoned image (BAIRN_TZ). Empty means the machine's
	// local zone.
	Zone string

	// LogFormat is "json" (cron-friendly) or "text" (interactive).
	LogFormat string
}

// Load reads config from environment variables, applying defaults
// where unset. Flag values from the caller win and may be merged
// in afterwards.
func Load() (*Config, error) {
	c := &Config{
		FamlyAccessToken: os.Getenv("FAMLY_ACCESS_TOKEN"),
		FamlyEmail:       os.Getenv("FAMLY_EMAIL"),
		FamlyPassword:    os.Getenv("FAMLY_PASSWORD"),
		FamlyDeviceID:    os.Getenv("FAMLY_DEVICE_ID"),
		FamlyBaseURL:     os.Getenv("FAMLY_BASE_URL"),
		ImmichBaseURL:    os.Getenv("IMMICH_BASE_URL"),
		ImmichAPIKey:     os.Getenv("IMMICH_API_KEY"),
		Albums:           envBool("BAIRN_ALBUMS"),
		AlbumMonth:       os.Getenv("BAIRN_ALBUM_MONTH"),
		AlbumYear:        os.Getenv("BAIRN_ALBUM_YEAR"),
		AlbumShare:       os.Getenv("BAIRN_ALBUM_SHARE"),
		SaveDir:          os.Getenv("BAIRN_SAVE_DIR"),
		StatePath:        os.Getenv("BAIRN_STATE_PATH"),
		Zone:             os.Getenv("BAIRN_TZ"),
		LogFormat:        os.Getenv("BAIRN_LOG_FORMAT"),
	}
	if c.LogFormat == "" {
		c.LogFormat = "json"
	}
	c.ProgressEvery = DefaultProgressEvery
	if v := os.Getenv("BAIRN_PROGRESS_EVERY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("config: BAIRN_PROGRESS_EVERY %q must be a whole number of pages, 0 or more", v)
		}
		c.ProgressEvery = n
	}
	if c.SaveDir == "" {
		dir, err := dataDir()
		if err != nil {
			return nil, fmt.Errorf("config: data dir: %w", err)
		}
		c.SaveDir, err = ensureDir(filepath.Join(dir, "assets"))
		if err != nil {
			return nil, fmt.Errorf("config: save dir: %w", err)
		}
	}
	if c.StatePath == "" {
		dir, err := stateDir()
		if err != nil {
			return nil, fmt.Errorf("config: state dir: %w", err)
		}
		c.StatePath = filepath.Join(dir, "state.json")
	}
	c.SessionPath = filepath.Join(filepath.Dir(c.StatePath), session.FileName)
	return c, nil
}

// Validate checks that required fields are set for the given run
// mode. fetch requires Famly auth and a save dir; Immich is
// optional (auto-enabled when both URL and key are present).
func (c *Config) Validate(mode string) error {
	if c.FamlyDeviceID != "" && !famly.ValidDeviceID(c.FamlyDeviceID) {
		return errors.New("config: FAMLY_DEVICE_ID must be a UUID (8-4-4-4-12 hex digits); unset it to use the stored per-install id")
	}
	switch mode {
	case "fetch":
		if c.FamlyAccessToken == "" && (c.FamlyEmail == "" || c.FamlyPassword == "") {
			return errors.New("config: set FAMLY_EMAIL and FAMLY_PASSWORD (recommended) or FAMLY_ACCESS_TOKEN; run \"bairn login\" to verify credentials")
		}
		if c.SaveDir == "" {
			return errors.New("config: fetch needs BAIRN_SAVE_DIR or --save-dir")
		}
		// Immich is optional. If one of the two vars is set, the
		// other must be too: otherwise the operator probably
		// intended Immich and forgot a value.
		if (c.ImmichBaseURL == "") != (c.ImmichAPIKey == "") {
			return errors.New("config: IMMICH_BASE_URL and IMMICH_API_KEY must both be set, or both unset")
		}
	case "status":
		// state path must be openable but no creds needed
	case "drift":
		// drift is multi-vendor (Famly + Immich). Per-vendor auth
		// checks happen at runDrift() once the manifest is loaded:
		// Famly's auth_env triggers the refreshing-token path,
		// other vendors flow through Probe's env-fallback.
	}
	return nil
}

// envBool reports whether name is set to 1, true, yes or on.
func envBool(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// stateDir returns $XDG_STATE_HOME/bairn or its fallback.
func stateDir() (string, error) {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return ensureDir(filepath.Join(v, "bairn"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return ensureDir(filepath.Join(home, ".local", "state", "bairn"))
}

// dataDir returns $XDG_DATA_HOME/bairn or its fallback. The save
// dir defaults to a subdirectory of this.
func dataDir() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return ensureDir(filepath.Join(v, "bairn"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return ensureDir(filepath.Join(home, ".local", "share", "bairn"))
}

func ensureDir(p string) (string, error) {
	if err := privdir.Ensure(p); err != nil {
		return "", err
	}
	return p, nil
}
