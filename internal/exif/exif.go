// Package exif reinjects metadata into JPEG files whose EXIF was
// stripped at the source.
//
// Bairn's archival contract requires every available context to land
// in the file itself; the operator should be able to copy a JPEG
// out of the save directory and have a third-party tool read the
// timestamp, caption, and attribution.
//
// Failures are logged and ignored: a JPEG without EXIF is still
// strictly more valuable than no JPEG. The save itself does not
// fail because EXIF reinjection failed.
package exif

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	jis "github.com/dsoprea/go-jpeg-image-structure/v2"

	exif "github.com/dsoprea/go-exif/v3"
	exifcommon "github.com/dsoprea/go-exif/v3/common"
	exifundef "github.com/dsoprea/go-exif/v3/undefined"
)

// SanitizeText collapses embedded line breaks and control characters
// to single spaces and trims surrounding whitespace.
//
// EXIF text fields (ImageDescription, UserComment) are flat strings;
// downstream metadata viewers commonly split on embedded newlines
// and render each line as a separate entry, so a multi-paragraph
// post body shows up as several "captions" rather than one. We
// flatten here. A future XMP layer can keep paragraph structure
// because XMP-dc:description is a structured array.
func SanitizeText(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		case unicode.IsControl(r):
			// Drop other control chars entirely.
			continue
		case r == ' ':
			if !prevSpace {
				b.WriteRune(r)
				prevSpace = true
			}
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// Fields is the set of EXIF/IFD tags and XMP properties bairn
// injects.
//
// Empty fields are skipped (a zero time, empty string, or nil
// pointer means "don't write"). DateTimeOriginal is the only
// strongly recommended field: without it, the image timeline in
// any photo viewer falls back to filesystem mtime, which travels
// less reliably than EXIF.
//
// XMP-specific fields are written as an APP1 segment alongside
// the EXIF APP1. Modern photo apps (Apple Photos, Lightroom,
// Immich) prefer XMP over EXIF for descriptions and keywords;
// older tooling reads EXIF. Writing both gives full coverage.
type Fields struct {
	// EXIF (legacy + ubiquitous).
	DateTimeOriginal   time.Time
	OffsetTimeOriginal string // e.g. "+00:00"
	ImageDescription   string
	UserComment        string
	Artist             string
	Software           string

	// GPS is optional; both must be set for either to apply.
	GPSLatitude  *float64
	GPSLongitude *float64

	// XMP (modern). Description is the full unsanitized body for
	// dc:description (XML can carry newlines and Unicode safely).
	// Keywords land as dc:subject Bag entries (one per element).
	XMPDescription string
	XMPKeywords    []string
}

// Reinject rewrites the EXIF blob of the JPEG at path with the
// fields supplied. Existing EXIF is preserved for any fields not
// set in Fields.
//
// Returns an error if the file is unreadable, not a JPEG, or the
// rewrite cannot be serialised. Callers should treat errors as
// non-fatal: the save itself remains good.
func Reinject(path string, f Fields) error {
	jmp := jis.NewJpegMediaParser()
	intfc, err := jmp.ParseFile(path)
	if err != nil {
		return fmt.Errorf("exif: parse %s: %w", path, err)
	}
	sl, ok := intfc.(*jis.SegmentList)
	if !ok {
		return fmt.Errorf("exif: parsed object is not a JPEG SegmentList")
	}

	rootIb, err := sl.ConstructExifBuilder()
	if err != nil {
		// File has no EXIF segment yet. Build one from scratch.
		im, e := exifcommon.NewIfdMappingWithStandard()
		if e != nil {
			return fmt.Errorf("exif: ifd mapping: %w", e)
		}
		ti := exif.NewTagIndex()
		rootIb = exif.NewIfdBuilder(im, ti, exifcommon.IfdStandardIfdIdentity, exifcommon.EncodeDefaultByteOrder)
	}

	if err := setIfd0(rootIb, f); err != nil {
		return fmt.Errorf("exif: set IFD0: %w", err)
	}
	if err := setExifIfd(rootIb, f); err != nil {
		return fmt.Errorf("exif: set EXIF IFD: %w", err)
	}
	if f.GPSLatitude != nil && f.GPSLongitude != nil {
		if err := setGPSIfd(rootIb, *f.GPSLatitude, *f.GPSLongitude); err != nil {
			return fmt.Errorf("exif: set GPS IFD: %w", err)
		}
	}

	if err := sl.SetExif(rootIb); err != nil {
		return fmt.Errorf("exif: SetExif: %w", err)
	}

	if err := setXMP(sl, f); err != nil {
		return fmt.Errorf("exif: setXMP: %w", err)
	}

	// Write the EXIF-updated JPEG to an in-memory buffer so we
	// can splice the XMP segment into the byte stream at the
	// correct pre-SOS position before persisting.
	var buf bytes.Buffer
	if err := sl.Write(&buf); err != nil {
		return fmt.Errorf("exif: write to buffer: %w", err)
	}
	finalBytes, err := spliceXMPIntoBytes(sl, buf.Bytes())
	if err != nil {
		return fmt.Errorf("exif: splice XMP: %w", err)
	}

	// Atomic tmp+rename for crash-safety.
	tmp := path + ".exif.tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("exif: open tmp %s: %w", tmp, err)
	}
	if _, err := out.Write(finalBytes); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("exif: write %s: %w", tmp, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("exif: fsync %s: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("exif: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("exif: rename %s: %w", tmp, err)
	}
	return nil
}

func setIfd0(rootIb *exif.IfdBuilder, f Fields) error {
	if f.ImageDescription == "" && f.Artist == "" && f.Software == "" {
		return nil
	}
	ib, err := exif.GetOrCreateIbFromRootIb(rootIb, "IFD")
	if err != nil {
		return err
	}
	if f.ImageDescription != "" {
		if err := ib.SetStandardWithName("ImageDescription", truncate(f.ImageDescription, 250)); err != nil {
			return err
		}
	}
	if f.Artist != "" {
		if err := ib.SetStandardWithName("Artist", f.Artist); err != nil {
			return err
		}
	}
	if f.Software != "" {
		if err := ib.SetStandardWithName("Software", f.Software); err != nil {
			return err
		}
	}
	return nil
}

func setExifIfd(rootIb *exif.IfdBuilder, f Fields) error {
	hasAny := !f.DateTimeOriginal.IsZero() || f.OffsetTimeOriginal != "" || f.UserComment != ""
	if !hasAny {
		return nil
	}
	ib, err := exif.GetOrCreateIbFromRootIb(rootIb, "IFD/Exif")
	if err != nil {
		return err
	}
	if !f.DateTimeOriginal.IsZero() {
		// EXIF wants "2006:01:02 15:04:05" (colons, not dashes, in date).
		s := f.DateTimeOriginal.UTC().Format("2006:01:02 15:04:05")
		if err := ib.SetStandardWithName("DateTimeOriginal", s); err != nil {
			return err
		}
	}
	if f.OffsetTimeOriginal != "" {
		// Best-effort: not all decoders read this. Cheap to write.
		_ = ib.SetStandardWithName("OffsetTimeOriginal", f.OffsetTimeOriginal)
	}
	if f.UserComment != "" {
		uc := encodeUserComment(f.UserComment)
		if err := ib.SetStandardWithName("UserComment", uc); err != nil {
			return err
		}
	}
	return nil
}

func setGPSIfd(rootIb *exif.IfdBuilder, lat, lng float64) error {
	ib, err := exif.GetOrCreateIbFromRootIb(rootIb, "IFD/GPSInfo")
	if err != nil {
		return err
	}
	latRef, latRat := degToRationals(lat, "N", "S")
	lngRef, lngRat := degToRationals(lng, "E", "W")
	if err := ib.SetStandardWithName("GPSVersionID", []uint8{2, 0, 0, 0}); err != nil {
		return err
	}
	if err := ib.SetStandardWithName("GPSLatitudeRef", latRef); err != nil {
		return err
	}
	if err := ib.SetStandardWithName("GPSLatitude", latRat); err != nil {
		return err
	}
	if err := ib.SetStandardWithName("GPSLongitudeRef", lngRef); err != nil {
		return err
	}
	if err := ib.SetStandardWithName("GPSLongitude", lngRat); err != nil {
		return err
	}
	return nil
}

// encodeUserComment wraps the text in EXIF's UserComment encoding.
// Pure ASCII uses the ASCII prefix; anything else uses UNICODE
// (UTF-16 BE), which any spec-compliant reader handles.
func encodeUserComment(text string) exifundef.Tag9286UserComment {
	if isASCII(text) {
		return exifundef.Tag9286UserComment{
			EncodingType:  exifundef.TagUndefinedType_9286_UserComment_Encoding_ASCII,
			EncodingBytes: []byte(text),
		}
	}
	// UTF-16 BE for the UNICODE encoding.
	codepoints := utf16.Encode([]rune(text))
	buf := make([]byte, 0, 2*len(codepoints))
	for _, c := range codepoints {
		buf = append(buf, byte(c>>8), byte(c&0xff))
	}
	return exifundef.Tag9286UserComment{
		EncodingType:  exifundef.TagUndefinedType_9286_UserComment_Encoding_UNICODE,
		EncodingBytes: buf,
	}
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}

// degToRationals encodes a decimal degree value as the EXIF
// rational triple (deg, min, sec) plus a single-letter ref.
// EXIF rationals are pairs of uint32 (numerator, denominator).
func degToRationals(deg float64, posRef, negRef string) (string, []exifcommon.Rational) {
	ref := posRef
	if deg < 0 {
		ref = negRef
		deg = -deg
	}
	d := uint32(deg)
	mFloat := (deg - float64(d)) * 60
	m := uint32(mFloat)
	s := (mFloat - float64(m)) * 60
	// Encode seconds with 4 decimal places of precision.
	const sDen = uint32(10000)
	sNum := uint32(s * float64(sDen))
	return ref, []exifcommon.Rational{
		{Numerator: d, Denominator: 1},
		{Numerator: m, Denominator: 1},
		{Numerator: sNum, Denominator: sDen},
	}
}

// truncate cuts s to at most n bytes at a UTF-8 char boundary, and
// preferentially at a recent word boundary so we don't slice a
// caption mid-word. If a word boundary is reachable within
// truncateWordWindow bytes of the byte limit, we cut there;
// otherwise we cut at the char boundary and append an ellipsis.
//
// Trailing whitespace and dangling punctuation are trimmed.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	wordCut := cut
	minCut := cut - truncateWordWindow
	if minCut < 0 {
		minCut = 0
	}
	for i := cut; i > minCut; i-- {
		if isWordBoundaryByte(s[i-1]) {
			wordCut = i - 1
			break
		}
	}
	out := strings.TrimRight(s[:wordCut], " \t,;.:")
	// We always truncated (input length exceeded n); append an
	// ellipsis so readers can tell the description was clipped
	// rather than ending at a natural break.
	return out + "…"
}

// truncateWordWindow is the maximum number of bytes we'll back
// off from the hard byte limit to land on a word boundary.
const truncateWordWindow = 32

func isWordBoundaryByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', ',', ';':
		return true
	}
	return false
}

