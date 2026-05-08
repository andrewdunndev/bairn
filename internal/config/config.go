// Package config loads bairn's configuration from flags, env vars,
// and an optional TOML file. Precedence: flags > env > file > zero.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gitlab.com/dunn.dev/bairn/api/famly"
)

// Config is the application-level configuration. Populated by Load.
type Config struct {
	// FamlyAccessToken is the session token (preferred for short
	// runs and CI).
	FamlyAccessToken string

	// FamlyEmail and FamlyPassword enable token refresh on 401.
	FamlyEmail    string
	FamlyPassword string

	// FamlyDeviceID is a stable per-host identifier sent to Famly's
	// Authenticate mutation. Optional; falls back to a constant.
	FamlyDeviceID string

	// FamlyBaseURL overrides the production endpoint. Tests only.
	FamlyBaseURL string

	// ImmichBaseURL is the Immich API root, e.g.
	// "https://immich.home.example/api".
	ImmichBaseURL string

	// ImmichAPIKey is the x-api-key header value.
	ImmichAPIKey string

	// SaveDir is the root for saved photos and videos.
	// Default: $XDG_DATA_HOME/bairn/assets
	SaveDir string

	// StatePath is the JSON state file path.
	// Default: $XDG_STATE_HOME/bairn/state.json
	StatePath string

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
		SaveDir:          os.Getenv("BAIRN_SAVE_DIR"),
		StatePath:        os.Getenv("BAIRN_STATE_PATH"),
		LogFormat:        os.Getenv("BAIRN_LOG_FORMAT"),
	}
	if c.FamlyDeviceID == "" {
		// Famly's DeviceId scalar validates as UUID. Use a stable
		// per-host derivation rather than a constant string.
		c.FamlyDeviceID = famly.DeriveDeviceID()
	}
	if c.LogFormat == "" {
		c.LogFormat = "json"
	}
	if c.SaveDir == "" {
		dir, err := dataDir()
		if err != nil {
			return nil, fmt.Errorf("config: data dir: %w", err)
		}
		c.SaveDir = filepath.Join(dir, "assets")
	}
	if c.StatePath == "" {
		dir, err := stateDir()
		if err != nil {
			return nil, fmt.Errorf("config: state dir: %w", err)
		}
		c.StatePath = filepath.Join(dir, "state.json")
	}
	return c, nil
}

// Validate checks that required fields are set for the given run
// mode. fetch requires Famly auth and a save dir; Immich is
// optional (auto-enabled when both URL and key are present).
func (c *Config) Validate(mode string) error {
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
		if c.FamlyAccessToken == "" && (c.FamlyEmail == "" || c.FamlyPassword == "") {
			return errors.New("config: drift needs FAMLY_EMAIL and FAMLY_PASSWORD (recommended) or FAMLY_ACCESS_TOKEN; the credentials path uses bairn's normal refreshing token, the static token path expires")
		}
	}
	return nil
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
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	return p, nil
}
