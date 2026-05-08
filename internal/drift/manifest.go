package drift

import (
	"fmt"
	"os"
	"regexp"

	"github.com/BurntSushi/toml"
)

// Endpoint describes a single probe target.
type Endpoint struct {
	ID       string `toml:"id"`
	Method   string `toml:"method"`
	Path     string `toml:"path"`
	BodyJSON string `toml:"body_json"`
}

// Manifest is the TOML-loaded probe configuration. Mirrors the
// schema discovery/probe/manifest.example.toml documents and the
// shape.py prototype reads.
type Manifest struct {
	BaseURL    string     `toml:"base_url"`
	AuthHeader string     `toml:"auth_header"`
	AuthEnv    string     `toml:"auth_env"`
	UserAgent  string     `toml:"user_agent"`
	DelaySec   float64    `toml:"delay_sec"`
	Endpoints  []Endpoint `toml:"endpoint"`
}

// LoadManifest reads a TOML manifest from path. base_url is
// env-expanded so an operator-private host (e.g. an Immich URL)
// can be supplied via env without committing it to the manifest.
func LoadManifest(path string) (*Manifest, error) {
	var m Manifest
	if _, err := toml.DecodeFile(path, &m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if m.BaseURL == "" {
		return nil, fmt.Errorf("manifest %s: base_url required", path)
	}
	expanded, err := ExpandEnv(m.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: base_url: %w", path, err)
	}
	m.BaseURL = expanded
	if m.AuthHeader == "" {
		return nil, fmt.Errorf("manifest %s: auth_header required", path)
	}
	if m.AuthEnv == "" {
		return nil, fmt.Errorf("manifest %s: auth_env required", path)
	}
	return &m, nil
}

var envRefRe = regexp.MustCompile(`\$\{([A-Z0-9_]+)\}`)

// ExpandEnv replaces ${VAR} references with environment values.
// Returns an error if any referenced variable is unset, with the
// missing name in the message.
func ExpandEnv(s string) (string, error) {
	var missing string
	out := envRefRe.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1]
		v := os.Getenv(name)
		if v == "" && missing == "" {
			missing = name
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("env var %s not set", missing)
	}
	return out, nil
}
