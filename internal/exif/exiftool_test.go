package exif

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestRoundTripViaExifTool writes a fixture JPEG via Reinject and
// then reads back the metadata using exiftool, the canonical
// reference implementation. Catches the next class of "I had to
// notice" issue: technically valid output that an actual photo
// viewer doesn't display correctly.
//
// Skipped when exiftool isn't on PATH so go test in a bare CI
// container stays green; install with `brew install exiftool` (mac)
// or `apt install libimage-exiftool-perl` (debian).
func TestRoundTripViaExifTool(t *testing.T) {
	bin, err := exec.LookPath("exiftool")
	if err != nil {
		t.Skip("exiftool not on PATH; skipping metadata round-trip test")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(path, makeJPEG(t, 32, 32), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	when := time.Date(2026, 5, 6, 18, 33, 21, 0, time.UTC)
	body := "Caption with newline\nand non-ASCII: café"
	fields := Fields{
		DateTimeOriginal:   when,
		OffsetTimeOriginal: "+00:00",
		ImageDescription:   "Flat description",
		Artist:             "Educator A",
		Software:           "bairn round-trip",
		XMPDescription:     body,
		XMPKeywords:        []string{"Child A", "Outdoor"},
	}
	if err := Reinject(path, fields); err != nil {
		t.Fatalf("Reinject: %v", err)
	}

	out, err := exec.Command(bin, "-G", "-a", "-s", "-j", path).Output()
	if err != nil {
		t.Fatalf("exiftool: %v", err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("decode exiftool output: %v\n%s", err, out)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(parsed))
	}
	tags := parsed[0]

	// Helper: find the value for a tag suffix across exiftool's
	// "Group:Tag" keys ("EXIF:Artist", "XMP-dc:Creator", etc.).
	findBySuffix := func(suffix string) (string, bool) {
		for k, v := range tags {
			if strings.HasSuffix(strings.ToLower(k), strings.ToLower(":"+suffix)) ||
				strings.EqualFold(k, suffix) {
				switch s := v.(type) {
				case string:
					return s, true
				case []any:
					if len(s) > 0 {
						if str, ok := s[0].(string); ok {
							return str, true
						}
					}
				}
			}
		}
		return "", false
	}
	hasInList := func(suffix string, want string) bool {
		for k, v := range tags {
			if strings.HasSuffix(strings.ToLower(k), strings.ToLower(":"+suffix)) {
				switch s := v.(type) {
				case string:
					return s == want
				case []any:
					for _, item := range s {
						if str, ok := item.(string); ok && str == want {
							return true
						}
					}
				}
			}
		}
		return false
	}

	// EXIF round-trip
	if got, _ := findBySuffix("ImageDescription"); got != "Flat description" {
		t.Errorf("ImageDescription = %q, want %q", got, "Flat description")
	}
	if got, _ := findBySuffix("Artist"); got != "Educator A" {
		t.Errorf("Artist = %q", got)
	}
	if got, _ := findBySuffix("Software"); got != "bairn round-trip" {
		t.Errorf("Software = %q", got)
	}
	if got, _ := findBySuffix("DateTimeOriginal"); !strings.Contains(got, "2026:05:06 18:33:21") {
		t.Errorf("DateTimeOriginal = %q", got)
	}
	if got, _ := findBySuffix("OffsetTimeOriginal"); got != "+00:00" {
		t.Errorf("OffsetTimeOriginal = %q", got)
	}

	// XMP round-trip
	if !hasInList("Subject", "Child A") {
		t.Errorf("XMP-dc:Subject does not contain 'Child A': %v", findKeys(tags, "subject"))
	}
	if !hasInList("Subject", "Outdoor") {
		t.Errorf("XMP-dc:Subject does not contain 'Outdoor'")
	}
	if got, _ := findBySuffix("Description"); !strings.Contains(got, "café") {
		t.Errorf("XMP-dc:Description should retain non-ASCII; got %q", got)
	}
	if got, _ := findBySuffix("Description"); !strings.Contains(got, "Caption with newline") {
		t.Errorf("XMP-dc:Description missing body: %q", got)
	}
	if got, _ := findBySuffix("Creator"); got != "Educator A" {
		t.Errorf("XMP-dc:Creator = %q", got)
	}
	if got, _ := findBySuffix("DateCreated"); !strings.Contains(got, "2026:05:06 18:33:21") {
		t.Errorf("XMP photoshop:DateCreated = %q", got)
	}
}

func findKeys(tags map[string]any, sub string) []string {
	out := []string{}
	for k := range tags {
		if strings.Contains(strings.ToLower(k), sub) {
			out = append(out, k)
		}
	}
	return out
}

// silence unused-import warnings in tooling that doesn't see test
// files using reflect
var _ = reflect.DeepEqual
