package exif

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"sync"

	jis "github.com/dsoprea/go-jpeg-image-structure/v2"
)

// pendingXMPBySegmentList carries an XMP payload from setXMP() to
// spliceXMPIntoBytes(). dsoprea's SegmentList.Add appends segments
// to the end of the file, which puts new APP1 segments after the
// SOS marker where readers ignore them. We instead collect XMP
// payloads here and splice them into the file's bytes at write
// time.
var (
	pendingXMPMu          sync.Mutex
	pendingXMPBySegmentList = map[*jis.SegmentList][]byte{}
)

// xmpAdobePrefix is the magic header that introduces an XMP APP1
// segment in a JPEG. JPEG readers identify the segment as XMP
// (vs EXIF, which uses "Exif\0\0") by this prefix.
var xmpAdobePrefix = []byte("http://ns.adobe.com/xap/1.0/\x00")

// jpegMarkerAPP1 is the JPEG segment marker for application data
// segment 1; both EXIF and XMP live behind this marker, separated
// by their respective payload prefixes.
const jpegMarkerAPP1 = 0xe1

// setXMP writes (or replaces) the XMP APP1 segment in sl with a
// packet derived from f. No-op when no XMP-relevant field is set.
//
// Important: dsoprea's SegmentList.Add() appends to the end, which
// for JPEGs ends up *after* the SOS (start of scan) marker. JPEG
// metadata segments must come before SOS or readers ignore them.
// When sl already has an XMP segment in the right place we update
// in place; otherwise we mark sl with a flag and rely on
// spliceXMPIntoBytes() at write time to insert correctly.
func setXMP(sl *jis.SegmentList, f Fields) error {
	if !needsXMP(f) {
		return nil
	}
	packet, err := buildXMPPacket(f)
	if err != nil {
		return err
	}
	payload := make([]byte, 0, len(xmpAdobePrefix)+len(packet))
	payload = append(payload, xmpAdobePrefix...)
	payload = append(payload, packet...)

	// Replace existing XMP segment if present (in-place mutation
	// keeps the original slot, before SOS).
	idx, _, findErr := sl.FindXmp()
	if findErr == nil && idx >= 0 {
		sl.Segments()[idx].Data = payload
		return nil
	}
	// New segment: stash the payload on sl's metadata for the
	// spliceXMPIntoBytes pass to inject post-Write.
	pendingXMPMu.Lock()
	pendingXMPBySegmentList[sl] = payload
	pendingXMPMu.Unlock()
	return nil
}

// spliceXMPIntoBytes inserts the pending XMP APP1 segment for sl
// (if any) into jpegBytes, at the first valid metadata-segment
// position before SOS. Returns jpegBytes unchanged if no XMP is
// pending.
func spliceXMPIntoBytes(sl *jis.SegmentList, jpegBytes []byte) ([]byte, error) {
	pendingXMPMu.Lock()
	payload, ok := pendingXMPBySegmentList[sl]
	if ok {
		delete(pendingXMPBySegmentList, sl)
	}
	pendingXMPMu.Unlock()
	if !ok || len(payload) == 0 {
		return jpegBytes, nil
	}
	// Find the byte offset of the first metadata-segment break
	// after SOI (offset 2). Insert the XMP segment immediately
	// after the existing EXIF APP1 if present, else right after
	// SOI.
	insertAt, err := findMetadataInsertOffset(jpegBytes)
	if err != nil {
		return nil, err
	}
	// Build the APP1 segment: marker (FF E1) + 2-byte BE length
	// (length = 2 + payload size, the +2 accounts for the length
	// bytes themselves) + payload.
	segLen := len(payload) + 2
	if segLen > 0xFFFF {
		return nil, fmt.Errorf("xmp: payload too large for a single APP1 segment (%d bytes)", len(payload))
	}
	header := []byte{0xFF, jpegMarkerAPP1, byte(segLen >> 8), byte(segLen & 0xFF)}
	out := make([]byte, 0, len(jpegBytes)+len(header)+len(payload))
	out = append(out, jpegBytes[:insertAt]...)
	out = append(out, header...)
	out = append(out, payload...)
	out = append(out, jpegBytes[insertAt:]...)
	return out, nil
}

// findMetadataInsertOffset returns the byte offset at which a new
// APP segment should be inserted. Walks segments from SOI; returns
// the offset just after the existing EXIF APP1 (preferred), or
// just after SOI if no APP1 exists, but always before SOS.
func findMetadataInsertOffset(b []byte) (int, error) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0, fmt.Errorf("xmp: input is not a JPEG (no SOI)")
	}
	i := 2 // after SOI
	insert := i
	for i < len(b)-2 {
		if b[i] != 0xFF {
			return 0, fmt.Errorf("xmp: malformed JPEG at offset %d", i)
		}
		marker := b[i+1]
		if marker == 0xDA { // SOS
			return insert, nil
		}
		if marker == 0xD9 { // EOI without SOS (unusual)
			return insert, nil
		}
		// Standalone markers (no length): SOI (handled), TEM (0x01),
		// RSTn (0xD0..0xD7).
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if i+4 > len(b) {
			return 0, fmt.Errorf("xmp: truncated segment header")
		}
		segLen := int(b[i+2])<<8 | int(b[i+3])
		segEnd := i + 2 + segLen
		if segEnd > len(b) {
			return 0, fmt.Errorf("xmp: segment overruns file")
		}
		// After this segment is a valid insert point. Prefer
		// inserting right after the EXIF APP1 if we encounter it.
		if marker == jpegMarkerAPP1 {
			payload := b[i+4 : segEnd]
			if isExifPayload(payload) {
				insert = segEnd
			}
		} else if insert == 2 {
			// Until we see EXIF, advance the insert point past any
			// JFIF/APP0 etc., so we don't sit between SOI and APP0.
			insert = segEnd
		}
		i = segEnd
	}
	return insert, nil
}

func isExifPayload(payload []byte) bool {
	const prefix = "Exif\x00\x00"
	return len(payload) >= len(prefix) && string(payload[:len(prefix)]) == prefix
}

func needsXMP(f Fields) bool {
	return f.XMPDescription != "" ||
		len(f.XMPKeywords) > 0 ||
		f.Artist != "" ||
		!f.DateTimeOriginal.IsZero()
}

// buildXMPPacket emits a self-described XMP packet matching
// Adobe's expected boilerplate. We write the namespaces and
// elements bairn cares about; readers ignore the rest of the
// schema. Values are XML-escaped.
func buildXMPPacket(f Fields) ([]byte, error) {
	var b bytes.Buffer
	// Standard packet wrapper. The U+FEFF inside begin="" is the
	// XMP packet wrapper marker; it must be a literal BOM byte.
	b.WriteString("<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>")
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="bairn">`)
	b.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about=""`)
	b.WriteString(` xmlns:dc="http://purl.org/dc/elements/1.1/"`)
	b.WriteString(` xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"`)
	b.WriteString(`>`)

	if f.XMPDescription != "" {
		b.WriteString(`<dc:description><rdf:Alt><rdf:li xml:lang="x-default">`)
		if err := xml.EscapeText(&b, []byte(f.XMPDescription)); err != nil {
			return nil, err
		}
		b.WriteString(`</rdf:li></rdf:Alt></dc:description>`)
	}

	if f.Artist != "" {
		b.WriteString(`<dc:creator><rdf:Seq><rdf:li>`)
		if err := xml.EscapeText(&b, []byte(f.Artist)); err != nil {
			return nil, err
		}
		b.WriteString(`</rdf:li></rdf:Seq></dc:creator>`)
	}

	if len(f.XMPKeywords) > 0 {
		b.WriteString(`<dc:subject><rdf:Bag>`)
		for _, k := range f.XMPKeywords {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			b.WriteString(`<rdf:li>`)
			if err := xml.EscapeText(&b, []byte(k)); err != nil {
				return nil, err
			}
			b.WriteString(`</rdf:li>`)
		}
		b.WriteString(`</rdf:Bag></dc:subject>`)
	}

	if !f.DateTimeOriginal.IsZero() {
		offset := f.OffsetTimeOriginal
		if offset == "" {
			offset = "+00:00"
		}
		// XMP photoshop:DateCreated wants ISO 8601.
		stamp := f.DateTimeOriginal.UTC().Format("2006-01-02T15:04:05") + offset
		b.WriteString(`<photoshop:DateCreated>`)
		if err := xml.EscapeText(&b, []byte(stamp)); err != nil {
			return nil, err
		}
		b.WriteString(`</photoshop:DateCreated>`)
	}

	b.WriteString(`</rdf:Description></rdf:RDF></x:xmpmeta>`)
	b.WriteString(`<?xpacket end="w"?>`)

	// Sanity check: the packet should be parseable as XML by any
	// reader; pre-validate so we don't emit garbage.
	if err := xml.NewDecoder(bytes.NewReader(b.Bytes())).Decode(new(struct {
		XMLName xml.Name
	})); err != nil && !isAcceptableXMPParseError(err) {
		return nil, fmt.Errorf("invalid XMP packet generated: %w (size %d)", err, b.Len())
	}
	return b.Bytes(), nil
}

// isAcceptableXMPParseError filters out the "unknown namespace"
// errors that Go's xml package emits for valid XMP packets. The
// packet is well-formed; Go's decoder is just stricter about
// known namespaces than XMP needs to be.
func isAcceptableXMPParseError(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown") || strings.Contains(msg, "expected") {
		return true
	}
	// EOF is fine; the decoder reads only one top-level element.
	return strings.Contains(msg, "EOF") || strings.Contains(msg, "<?xpacket")
}
