# ADR 0005: archival posture, no sidecars

**Status**: accepted, 2026-05-07

## Context

bairn moves family-relevant photos and videos out of a vendor
surface (Famly) and into the operator's own storage. The vendor
strips EXIF on upload; bairn has the metadata Famly returned over
the API and can reinject it.

The question: where does the metadata live in the resulting
artefact?

Two camps:

- **Sidecar files**: a `.json` or `.xmp` next to the photo that
  carries the original metadata. Simple to produce, easy to read
  back. Loses on file-move-without-sidecar.
- **In-file embedding**: rewrite the image's EXIF/XMP/IPTC blocks
  to carry everything we know. Tooling-friendly with any EXIF-aware
  reader. Survives copies and transforms that don't strip metadata.

bairn is an archival tool, not a privacy gateway. The downstream
question of "should this metadata travel with the file" is the
operator's or the next consumer's call; bairn's job is to capture
everything available.

## Decision

**No sidecar files.** Per-asset metadata is embedded directly into
the artefact:

- **JPEG/TIFF**: full EXIF/XMP/IPTC payload via dsoprea/go-exif/v3.
- **Video (MP4/MOV)**: filename carries the timestamp; filesystem
  mtime is set from the source createdDate. No in-file metadata
  rewrite for video in v1.

Fields embedded into JPEG/TIFF, all when available, no flag-gating
for the privacy-related ones:

| EXIF/XMP tag         | Source                                        |
|----------------------|-----------------------------------------------|
| DateTimeOriginal     | `image.createdAt.date` (fallback feed item)   |
| OffsetTimeOriginal   | `+00:00` (Famly serves UTC)                   |
| ImageDescription     | feed item body, truncated to 250 chars        |
| UserComment          | full body + " - " + sender.name (Unicode-safe)|
| Artist               | sender.name (educator who posted)             |
| Software             | `bairn 0.x.y`                                 |
| GPSCoordinates       | optional, off until the operator supplies coordinates |
| XMP-dc:subject (Keywords) | per-image kid tag names where Famly tagged |

GPS is the one optional field, because bairn does not know the
photo's location; the operator supplies it explicitly if at all.

## Considered

- **Sidecar `.json` files.** Trivial to produce; lose on
  file-move and on most third-party tooling that doesn't know to
  look for them. Rejected.
- **XMP sidecar (`.xmp`).** Standard format, broader tooling
  support than `.json`. Still loses on move; in-file embedding is
  strictly better for the same data. Rejected.
- **Flag-gate child tag names.** Would require a per-deployment
  decision the operator has to remember. Rejected; the archival
  contract is clearer if all available context is embedded.

## Consequences

- Files copied off bairn's save directory carry their context with
  them. Useful when shipping to a friend, importing into another
  photo manager, or recovering from a backup tape.
- Operators who want to strip metadata before sharing must do so
  explicitly with `exiftool` or similar. That's the right place
  for that decision; not bairn's.
- Video metadata is comparatively bare. If a future user demands
  in-file video metadata rewrite, that's a focused follow-on
  using a Go MP4 atom library.
- Privacy-aware downstream consumers (Immich's facial recognition,
  shared albums, etc.) can read the embedded metadata; that's by
  design. bairn's job ends at "the file has the data."

## Revisit when

- Video metadata rewrite becomes a concrete requirement.
- The operator runs into a downstream tool that misbehaves on the
  embedded payload (large UserComment, Unicode collation, etc.).
- HEIC adoption at the source vendor pushes us off go-exif.
