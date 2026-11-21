package main

import (
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
)

func TestSoftware(t *testing.T) {
	tests := map[string]string{
		"dev":                  "bairn dev",
		"v0.6.0":               "bairn 0.6.0",
		"0.6.0":                "bairn 0.6.0",
		"v0.5.0-3-gabc1-dirty": "bairn 0.5.0-3-gabc1-dirty",
		"abc1234":              "bairn abc1234",
	}
	for in, want := range tests {
		if got := software(in); got != want {
			t.Errorf("software(%q) = %q, want %q", in, got, want)
		}
	}
	if software(Version) != "bairn dev" {
		t.Errorf("default Version should read dev, got %q", Version)
	}
}

func TestResolveVersion(t *testing.T) {
	mk := func(mod string, rev string) *debug.BuildInfo {
		bi := &debug.BuildInfo{Main: debug.Module{Version: mod}}
		if rev != "" {
			bi.Settings = []debug.BuildSetting{{Key: "vcs.revision", Value: rev}}
		}
		return bi
	}
	tests := []struct {
		name    string
		version string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"stamped wins", "v0.6.1", mk("v9.9.9", ""), true, "v0.6.1"},
		{"module version", "dev", mk("v0.6.1", "abc"), true, "v0.6.1"},
		{"devel falls to revision", "dev", mk("(devel)", "0123456789abcdef"), true, "0123456789ab"},
		{"nothing known", "dev", mk("(devel)", ""), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
	}
	for _, tc := range tests {
		if got := resolveVersion(tc.version, tc.info, tc.ok); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestVersionBeforeConfig(t *testing.T) {
	// `bairn version` must not need or create config directories.
	home := t.TempDir()
	cmd := exec.Command("go", "run", ".", "--version")
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+home+"/d", "XDG_STATE_HOME="+home+"/s", "XDG_CONFIG_HOME="+home+"/c")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.HasPrefix(string(out), "bairn ") {
		t.Errorf("output %q lacks the bairn prefix", out)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("version created directories: %v", entries)
	}
}
