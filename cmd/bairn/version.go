package main

import (
	"runtime/debug"
	"strings"
)

// resolveVersion returns the version to report. A release build stamps
// Version through -ldflags; a "dev" build (go install, go build) falls
// back to the module version Go recorded, then the VCS revision.
func resolveVersion(version string, info *debug.BuildInfo, ok bool) string {
	if version != "dev" || !ok || info == nil {
		return version
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			rev := s.Value
			if len(rev) > 12 {
				rev = rev[:12]
			}
			return rev
		}
	}
	return version
}

// versionLine is the text of `bairn version`.
func versionLine() string {
	info, ok := debug.ReadBuildInfo()
	return "bairn " + strings.TrimPrefix(resolveVersion(Version, info, ok), "v")
}
