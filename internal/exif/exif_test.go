package exif

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jis "github.com/dsoprea/go-jpeg-image-structure/v2"
)

// makeJPEG returns the bytes of a tiny solid-colour JPEG. EXIF-less.
func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{0xff, 0x80, 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buf.Bytes()
}

func TestReinjectFromScratch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(path, makeJPEG(t, 32, 32), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	when := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)
	fields := Fields{
		DateTimeOriginal:   when,
		OffsetTimeOriginal: "+00:00",
		ImageDescription:   "Great day at the park",
		UserComment:        "captioned post - Educator Name",
		Artist:             "Educator Name",
		Software:           "bairn 0.1",
	}
	if err := Reinject(path, fields); err != nil {
		t.Fatalf("Reinject: %v", err)
	}

	// Read back and verify the tags landed.
	tags := dumpExif(t, path)
	for _, want := range []string{"ImageDescription", "Artist", "Software", "DateTimeOriginal", "UserComment"} {
		if !contains(tags, want) {
			t.Errorf("expected tag %s in EXIF, got: %v", want, tagNames(tags))
		}
	}
	expectValue(t, tags, "ImageDescription", "Great day at the park")
	expectValue(t, tags, "Artist", "Educator Name")
	expectValue(t, tags, "Software", "bairn 0.1")
	expectValue(t, tags, "DateTimeOriginal", "2026:05:06 14:30:00")
}

func TestReinjectGPS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(path, makeJPEG(t, 16, 16), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	lat := 42.9634  // Grand Rapids, MI
	lng := -85.6681
	if err := Reinject(path, Fields{
		DateTimeOriginal: time.Now(),
		GPSLatitude:      &lat,
		GPSLongitude:     &lng,
	}); err != nil {
		t.Fatalf("Reinject: %v", err)
	}
	tags := dumpExif(t, path)
	if !contains(tags, "GPSLatitude") {
		t.Errorf("expected GPSLatitude in EXIF, got: %v", tagNames(tags))
	}
	if !contains(tags, "GPSLongitudeRef") {
		t.Error("expected GPSLongitudeRef in EXIF")
	}
}

func TestReinjectNonASCIIComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(path, makeJPEG(t, 16, 16), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := Reinject(path, Fields{
		DateTimeOriginal: time.Now(),
		UserComment:      "Café - résumé - éducateur",
	}); err != nil {
		t.Fatalf("Reinject: %v", err)
	}
	tags := dumpExif(t, path)
	if !contains(tags, "UserComment") {
		t.Error("expected UserComment in EXIF")
	}
}

func TestReinjectIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(path, makeJPEG(t, 16, 16), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	when := time.Date(2026, 5, 6, 14, 30, 0, 0, time.UTC)
	f := Fields{DateTimeOriginal: when, Software: "bairn 0.1"}
	if err := Reinject(path, f); err != nil {
		t.Fatalf("first Reinject: %v", err)
	}
	if err := Reinject(path, f); err != nil {
		t.Fatalf("second Reinject (re-applying same fields): %v", err)
	}
	tags := dumpExif(t, path)
	expectValue(t, tags, "Software", "bairn 0.1")
}

func TestReinjectTruncatesLongDescription(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(path, makeJPEG(t, 16, 16), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	long := strings.Repeat("abc ", 200) // 800 chars
	if err := Reinject(path, Fields{
		DateTimeOriginal: time.Now(),
		ImageDescription: long,
	}); err != nil {
		t.Fatalf("Reinject: %v", err)
	}
	tags := dumpExif(t, path)
	for _, tag := range tags {
		if tag.tagName == "ImageDescription" {
			if len(tag.value) > 250 {
				t.Errorf("ImageDescription length = %d, expected ≤ 250", len(tag.value))
			}
			return
		}
	}
	t.Error("ImageDescription not present")
}

// dumpExifTag is a minimal projection of a single tag.
type dumpExifTag struct {
	tagName string
	value   string
}

func dumpExif(t *testing.T, path string) []dumpExifTag {
	t.Helper()
	jmp := jis.NewJpegMediaParser()
	intfc, err := jmp.ParseFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sl := intfc.(*jis.SegmentList)
	_, _, exifTags, err := sl.DumpExif()
	if err != nil {
		t.Fatalf("DumpExif: %v", err)
	}
	out := make([]dumpExifTag, 0, len(exifTags))
	for _, et := range exifTags {
		out = append(out, dumpExifTag{tagName: et.TagName, value: et.FormattedFirst})
	}
	return out
}

func contains(tags []dumpExifTag, name string) bool {
	for _, t := range tags {
		if t.tagName == name {
			return true
		}
	}
	return false
}

func tagNames(tags []dumpExifTag) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.tagName
	}
	return out
}

func TestTruncateWordBoundary(t *testing.T) {
	cases := []struct {
		name     string
		s        string
		n        int
		wantHas  string
		wantNot  string
	}{
		{
			name:    "cuts at last space within window",
			s:       "The quick brown fox jumps over the lazy dog and runs",
			n:       30,
			wantHas: "jumps…",
			wantNot: "ove", // "over" got cut, only complete words remain
		},
		{
			name:    "appends ellipsis when significant trim",
			s:       "Word1 word2 word3 word4 word5 word6 word7 word8",
			n:       25,
			wantHas: "…",
		},
		{
			name:    "no truncation when under limit",
			s:       "short",
			n:       100,
			wantHas: "short",
		},
		{
			name:    "trims dangling punctuation",
			s:       "first sentence; second sentence with much more text",
			n:       18,
			wantNot: ";",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncate(c.s, c.n)
			if c.wantHas != "" && !strings.Contains(got, c.wantHas) {
				t.Errorf("truncate(_, %d) = %q, want substring %q", c.n, got, c.wantHas)
			}
			if c.wantNot != "" && strings.Contains(got, c.wantNot) {
				t.Errorf("truncate(_, %d) = %q, should not contain %q", c.n, got, c.wantNot)
			}
		})
	}
}

func TestReinjectWritesXMP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jpg")
	if err := os.WriteFile(path, makeJPEG(t, 16, 16), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	when := time.Date(2026, 5, 6, 18, 33, 21, 0, time.UTC)
	if err := Reinject(path, Fields{
		DateTimeOriginal:   when,
		OffsetTimeOriginal: "+00:00",
		Artist:             "Educator A",
		XMPDescription:     "Multi-line\nbody with newlines\nand non-ASCII: café",
		XMPKeywords:        []string{"Child A", "Child B", "Outdoor"},
	}); err != nil {
		t.Fatalf("Reinject: %v", err)
	}
	// Verify XMP packet is in the file. Look for our identifying
	// markers.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := []string{
		"x:xmpmeta",
		`xmlns:dc="http://purl.org/dc/elements/1.1/"`,
		"<dc:description>",
		"<rdf:li xml:lang=\"x-default\">",
		"<dc:creator>",
		"Educator A",
		"<dc:subject>",
		"<rdf:li>Child A</rdf:li>",
		"<rdf:li>Child B</rdf:li>",
		"<rdf:li>Outdoor</rdf:li>",
		"<photoshop:DateCreated>2026-05-06T18:33:21+00:00",
	}
	for _, w := range want {
		if !strings.Contains(string(body), w) {
			t.Errorf("XMP packet missing %q", w)
		}
	}
}

func TestSanitizeText(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello world", "hello world"},
		{"line one\nline two", "line one line two"},
		{"line one\r\nline two", "line one line two"},
		{"too   much    space", "too much space"},
		{"  trim me  ", "trim me"},
		{"tab\there", "tab here"},
		{"control\x07char", "controlchar"}, // BEL stripped
		{"", ""},
		{"\n\n\n", ""},
		{"keep — em dashes", "keep — em dashes"},
		{"unicode: café", "unicode: café"},
		{"multi\n\nparagraphs\n\nflattened", "multi paragraphs flattened"},
	}
	for _, c := range cases {
		got := SanitizeText(c.in)
		if got != c.want {
			t.Errorf("SanitizeText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func expectValue(t *testing.T, tags []dumpExifTag, name, want string) {
	t.Helper()
	for _, tag := range tags {
		if tag.tagName == name {
			if !strings.Contains(tag.value, want) {
				t.Errorf("%s = %q, want substring %q", name, tag.value, want)
			}
			return
		}
	}
	t.Errorf("tag %s missing", name)
}
