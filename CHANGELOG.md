# Changelog

All notable changes to `bairn` are documented here.

The format is based on [Keep a Changelog][kac], and this project
adheres to [Semantic Versioning][semver]. Pre-1.0 releases (`0.x`)
treat the **minor** position as the breaking-change marker: bumps
from `0.4.x` to `0.5.0` may include user-visible behavior or flag
changes; patch bumps within `0.x.y` are bug fixes only.

[kac]: https://keepachangelog.com/en/1.1.0/
[semver]: https://semver.org/spec/v2.0.0.html

## [Unreleased]

### Changed

- The default save root and state directory (under the XDG data and state
  dirs) are set to mode 0700 even when they already existed at a looser
  mode. A directory you choose with `--save-dir`, `BAIRN_SAVE_DIR` or
  `BAIRN_STATE_PATH` is created at 0700 if missing but never chmodded;
  bairn logs a warning when it is accessible to group or other.
- `main.Version` defaults to `dev`; `make build` targets stamp it from
  `git describe`. The EXIF Software tag reads `bairn 0.6.0` for a `v0.6.0`
  tag and `bairn dev` for an unstamped build, including a build from a
  source tarball with no `.git`. `make build VERSION=v0.6.0` overrides it.
- Every client that carries a credential (Famly login, Famly API, drift
  probe, Immich, the contract and smoke gates) refuses a redirect to another host or from https to
  http, and stops after 10 redirects. Immich previously refused all
  redirects; same-host ones now follow.
- A 401 or 403 from Immich, or five failed uploads in a row, stops
  uploads for the rest of the run instead of retrying every asset; the
  rest are saved to disk, counted in `uploadFailed`, and a rerun
  uploads them. A 403 now reports as unauthorized.
- `bairn fetch` logs the zone it will use for zoneless posts.
- Scheduled pipelines run only `drift-gate`; every other job sits behind
  an include that skips schedules.
- `bairn drift --diff` exits 2 when a manifest endpoint has no readable
  baseline file, instead of printing `ok`.
- Drift shapes walk every feed item and every nesting level (no
  5-item sample, no depth cap), so tag fields now appear. Reseed
  `discovery/baselines/main/` from the next schedule artifact.
- A response field that arrives as a different container than bairn's
  struct declares is recorded as `<object>` or `<array>`, so vendor
  keys never reach a shape. The drift artifact is developer-only.
- Drift documentation lives in `discovery/baselines/main/README.md`.

### Removed

- `discovery/probe/shape.py`, superseded by `bairn drift`.

### Fixed

- Images Famly labels "UTC" are dated in `--tz` / `BAIRN_TZ` (default:
  the local zone) like zoneless ones: Famly stores the instant, not
  where the photo was taken, so a Detroit nursery's photos no longer
  land 4-5 hours late, or on the next day, in Immich's timeline.
- Neither client follows a redirect that would replay its credential
  header to another host (Immich follows none, Famly stays on its host).
- An image with no zone of its own is dated in the local zone (as videos
  are), not as a UTC wall clock.
- A download error no longer carries the signed CDN query string.
- Retry-from-disk refuses a recorded path outside the save directory.
- The state lock lives on `<state-path>.lock`; on the state file itself it
  was lost at the first flush, so a second `bairn fetch` could run.
- A video's `.xmp` sidecar is written before the media file, and added
  beside media already on disk, so a retry-from-disk never lacks it.
- The save path is refused when a vendor id would resolve outside the
  save directory.
- State, config and archive directories are created 0700.
- A `Retry-After` longer than the 30s backoff cap is honoured up to five
  minutes; a longer one ends the retries instead of hammering a 429.
- A feed walk whose cursor stops moving ends with `feed cursor did not
  advance` instead of looping on the same page.
- A video in a post with no zoned image was dated as UTC, so it showed
  hours late in the timeline. It now uses the zone of the running
  machine (override with `--tz` or `BAIRN_TZ`), with the offset
  computed at the post's date.
- The Immich upload no longer sends `deviceId` / `deviceAssetId`;
  Immich 3.x dropped them. The Immich floor is now v3.0.2, and
  `IMMICH_VERSION` is pinned to 3.0.2, the version the home server
  runs.
- A failed Immich upload is retried on the next run. Assets saved to
  disk but not confirmed in Immich (a failed upload, an Immich outage,
  or an earlier `--no-immich` run) are uploaded from the disk sink,
  with the video `.xmp` sidecar, instead of being skipped. `duplicate`
  counts as confirmed. The fetch summary gains `uploadDuplicates` and
  `uploadFailed`, and `bairn fetch` exits 1 while any upload failed.
  Entries in existing state files without `uploadedAt` count as not
  uploaded.
- The state file is flushed every 50 changes and on exit instead of
  being rewritten and fsynced on every change, which was quadratic
  over a full history.

### Added

- `--tz` / `BAIRN_TZ` set the fallback zone for video dates. `--max-pages
  0` (unlimited) is documented and tested as the way to walk a full
  history.
- Both the Famly and Immich clients retry transport errors, 429
  (honouring `Retry-After`) and 5xx with capped exponential backoff and
  jitter; other 4xx fail at once. A retried Immich upload is safe: the
  server dedupes on the file checksum per owner and answers
  `duplicate`.
- A Famly 401 mid-run refreshes the token once through the token
  source (ADR 0003) and retries; a second 401 fails.
- The feed walk pauses one second between pages.
- CI: a weekly scheduled `drift-gate` run against the operator's own
  Famly account (two GETs). Drift diffing now treats `<empty>` and
  `null` shapes as wildcards and merges nested shapes across
  feed items, so a week with photos is not reported as drift.
- `IMMICH_VERSION` in the Makefile names the Immich release bairn is
  verified against via `make pre-tag-check`; Renovate tracks it with
  automerge off.
- XMP `digiKam:TagsList` (flat child names, same source as
  `dc:subject`) in the photo packet; Immich reads it and ignores
  `dc:subject`.
- Videos now carry metadata: a standalone `<file>.xmp` (description,
  date with its offset, tags) is written beside each video and sent to
  Immich as `sidecarData`. Photos still embed their XMP and get no
  sidecar.

### Fixed

- `bairn drift` exits 2 when an endpoint errors, answers non-2xx, or
  serves a non-JSON page, instead of passing on the endpoints that
  did answer. With `--anonymize` it no longer prints response sizes.
- A merged array shape keeps its `<n=*>` marker when the first sampled
  item had an empty array.
- The video sidecar also carries `exif:DateTimeOriginal`, the date tag
  Immich reads, and its offset comes from the zone of a sibling image
  in the post instead of a fixed `+00:00`. An image's offset is now
  evaluated at the instant that is stamped.
- EXIF `DateTimeOriginal` and XMP `photoshop:DateCreated` now carry the
  wall-clock time in the labelled offset. Previously the UTC clock
  reading was written next to a non-UTC offset, so the label disagreed
  with the time. Files already archived keep the old values.

### Removed

- CI: the `drift-gate-immich` and `smoke-immich` jobs (web/api only,
  never reachable from the runner), the Immich drift manifest and
  its empty baseline, and the `claude-drift-triage` include. `make
  pre-tag-check` remains the Immich gate.
- The vendored Immich `openapi.json`, the unused generated `imapi`
  client, the oapi-codegen config, `make gen-immich` and
  `make refresh-immich-spec`, and the oapi-codegen `go.mod` tool.
  The hand-written `api/immich` client and its contract test stay.

## [0.5.0] - 2026-05-09

This release rebases bairn's CI onto the `dunn.dev/pipeline@2.0.3`
catalog overhaul. Tooling (golangci-lint, govulncheck, syft,
cosign, Go itself) is now provably pinned to the catalog tag
bairn references; previously templates floated tooling via
`:latest` regardless of catalog pin. No user-facing behavior
changes; the binary, archive format, and CLI surface are
identical to v0.4.6.

### Added

- Multi-arch container image **scaffold** (single-arch by default
  in v0.5.0; flip to multi-arch in v0.5.1 once storr runner's
  qemu-user-static support is verified). Catalog template supports
  `multi_arch: true`; bairn's wiring is in place but conservative.

- Cosign-signed container image: every pushed `cli:vX.Y.Z` is
  signed by digest via GitLab OIDC keyless. Verify with:
  ```
  cosign verify registry.gitlab.com/dunn.dev/bairn/cli:vX.Y.Z \
    --certificate-identity-regexp '...'  --certificate-oidc-issuer ...
  ```

- `linux/arm64` binary: `bairn-linux-arm64` lands in releases
  alongside `bairn-linux-amd64` and `bairn-darwin-arm64`. The
  catalog's `go-release-binary` v2.0.0 default matrix added it.

- Per-binary `.sha256` sidecars: each released binary uploads
  alongside a `.sha256` checksum file (replaces the consolidated
  `checksums.txt` from the v1.x catalog).

### Changed

- All catalog includes bumped to `@2.0.3`. See
  `dunn.dev/pipeline` CHANGELOG.md for the catalog overhaul
  scope (component context interpolation, parallel:matrix,
  module cache, input validation, multi-arch container builds,
  ci-runtime-go runtime image).

- `Containerfile` collapsed from a multi-stage Go build to a
  thin runtime layer over `ci-runtime-go` (UBI micro). The
  cosign-signed binary in the package registry is now
  byte-for-byte the binary inside the container image — no
  recompile, no parity gap.

- CI cross-compile now runs as `parallel:matrix` (one job per
  target). Logs, retries, and module cache isolated per target.
  Faster overall pipeline; failures point to the exact arch
  that broke.

- Module cache via `cache:key:files: [go.sum]` keyed per target,
  warmed from main via `fallback_keys`. First-run feature
  branches no longer cold-download every Go module.

- `workflow:auto_cancel: on_new_commit: interruptible`: build/
  test jobs auto-cancel when an MR pushes new commits. Release-
  stage jobs (signing, package upload) opt out via the catalog's
  `interruptible: false` and continue once started.

- `smoke-immich` and `drift-gate-immich` jobs no longer run on
  tag pipelines (they ran with `allow_failure: true` because
  the storr runner cannot reach a homelab Immich; the red Xs
  on every release were noise that trained operators to ignore
  the signal). They still run on web/api triggers (operator-
  initiated, can pre-set credentials). The local
  `make pre-tag-check` is the actual tag-time gate.

- `test`, `drift-gate`, `smoke-immich`, `drift-gate-immich` now
  pull `ci-go:2.0.3` (was `:latest`). Pinned tooling matches
  the catalog version bairn references.

### Fixed

- `internal/drift/filter.go`: `reflect.Ptr` → `reflect.Pointer`
  (govet inline analyzer; `Ptr` is a deprecated alias).
- `internal/contract/immich.go`: rewrote a negated boolean
  expression by De Morgan's law (staticcheck `QF1001`).
- `internal/drift/filter_test.go`: dropped unused `raw` field on
  the `customTime` test fixture (made struct empty, which is the
  truthful model of a custom-unmarshalled opaque type).

These three lint hits had been masked since v0.4.6 by the
catalog v1.x pattern of pulling
`docker.io/golangci/golangci-lint:latest-alpine` with
`allow_failure: true` (to absorb docker.io rate-cap pull
flakes). With v2.0.0's `go-lint` template (backed by ci-go),
lint is a real gate.

## [0.4.6] - 2026-05-08

### Added

- `bairn smoke immich`: round-trip wire-contract gate. Logs in,
  mints an ephemeral API key, uploads a 1-pixel JPEG via the
  production `sink/immich` code path, asserts created, deletes
  the asset, deletes the API key. ~5 HTTP calls, ~250ms. Catches
  controller-layer class-validator enforcement that no static spec
  models.

- `make pre-tag-check`: new local gate (`test` + `smoke-immich`).
  Treat as the contract before `git tag`. Operator-side because
  the storr CI runner cannot reach a typical homelab Immich (LAN
  service, no NAT hairpin).

- `make smoke-immich` and `make refresh-immich-validator`:
  wrappers around the round-trip and probe-only modes
  respectively. Probe-only captures a static manifest of required
  fields for diagnostics.

- CI: `smoke-immich` and `drift-gate-immich` jobs (`allow_failure:
  true` until the runner can reach a target Immich).

### Notes

- v0.4.6 is the gate that v0.4.3 lacked. It would have caught the
  device-field regression before the tag shipped.

## [0.4.5] - 2026-05-08

### Fixed

- Restored `deviceId` / `deviceAssetId` upload fields. v0.4.3
  dropped them based on the vendored Immich OpenAPI spec, which
  doesn't list them, but the live Immich server enforces them via
  controller-layer class-validator decorators that no static spec
  captures. Andrew DeJong reproduced v0.4.3 failing on his Immich
  v2.7.5 with `HTTP 400 deviceId/deviceAssetId must be a string`.
  See `internal/contract/immich.go` for the gate that prevents
  this class of regression going forward.

## [0.4.4] - PULLED

This tag was published, then deleted within the same day. The
release shipped a container-only build with no functional
behavior change for binary consumers. Pulled together with
v0.4.3 because it was downstream of the v0.4.3 regression.

The container image `registry.gitlab.com/dunn.dev/bairn/cli:v0.4.4`
was unpublished. If you have a local pull, replace with `v0.4.5`
or later.

## [0.4.3] - PULLED

This tag was published, then deleted within the same day. The
release dropped `deviceId` / `deviceAssetId` from the Immich
upload payload based on the vendored OpenAPI spec, which broke
uploads against Immich v2.7.5. The binary, container, and SLSA
provenance package were all unpublished.

If you upgraded to v0.4.3, downgrade to v0.4.5 or later.

## [0.4.2] - 2026-05-08

### Added

- UX guardrails for silent zero-result fetches: bairn now exits
  non-zero with a clear message when the feed walk completes
  without writing any new files (previously: silent success that
  hid auth-token expiry and feed-shape changes).

## [0.4.1] - 2026-05-08

### Added

- `bairn drift --manifest <toml> --diff <baseline-dir>`: multi-
  vendor drift gate. Previously drift only ran against Famly's
  parent-side surface; v0.4.1 generalizes to any manifest of
  vendor endpoints, with Immich as the second consumer.

## [0.4.0] - 2026-05-08

### Added

- `--source famly|immich|all` enum on `bairn fetch`. Replaces the
  previous implicit Famly-only mode.

### Changed

- Immich upload payload migrated to v2.7.5+ wire format (zod
  migration in upstream Immich PR #26597). Wraps `metadata` as an
  array of objects rather than a single object. Fix contributed by
  Andrew DeJong (MR !1).

## [0.3.1] - 2026-04-29

### Changed

- CI: bumped catalog includes to `dunn.dev/pipeline@v1.6.0`,
  consumed the new pre-baked `ci-go` image (Go toolchain +
  govulncheck + syft + cosign in one authenticated pull).

## [0.3.0] - 2026-04-28

### Changed

- `bairn drift`: shape signatures now scoped to bairn's typed
  decoder surface (only fields the decoder reads). Vendor-side
  keys bairn ignores at decode time no longer trigger drift
  diffs. Reduces false-positive churn against Famly.

## [0.2.5] - 2026-04-25

### Added

- Initial drift baseline at `discovery/baselines/main/` for
  `bairn drift` to diff against.

## [0.2.4] - 2026-04-24

### Added

- `bairn drift --anonymize`: masks array cardinality and other
  request-shape signals so the drift output can be safely sent
  to Anthropic's Claude API for triage classification.

## [0.2.3] - 2026-04-22

### Changed

- CI: bumped catalog includes to `dunn.dev/pipeline@v1.5.2`.

## [0.2.2] - 2026-04-21

### Fixed

- Version bump only (release pipeline reproducibility).

## [0.2.1] - 2026-04-21

### Fixed

- `bairn drift`: validates credentials-or-token, errors on empty
  token instead of silently authenticating-then-401'ing.

## [0.2.0] - 2026-04-20

### Added

- `bairn drift`: native Go subcommand for vendor-shape drift
  detection. Replaces the prior bash + jq prototype. Fires on tag
  pipelines as a pre-release gate; non-empty output blocks the
  tag.

## [0.1.0] - 2026-04-19

### Added

- Initial release: `bairn fetch` walks Famly's parent-side feed,
  saves photos and videos to disk with EXIF/XMP metadata embedded,
  optionally pushes to Immich as a secondary sink.
- Full supply-chain release flow via `dunn.dev/pipeline` catalog:
  cosign-signed binaries, CycloneDX SBOM per binary, SLSA v1.0
  provenance, GitLab Release with all artifacts linked, OCI
  container image.

[Unreleased]: https://gitlab.com/dunn.dev/bairn/-/compare/v0.5.0...main
[0.5.0]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.5.0
[0.4.6]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.4.6
[0.4.5]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.4.5
[0.4.2]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.4.2
[0.4.1]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.4.1
[0.4.0]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.4.0
[0.3.1]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.3.1
[0.3.0]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.3.0
[0.2.5]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.2.5
[0.2.4]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.2.4
[0.2.3]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.2.3
[0.2.2]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.2.2
[0.2.1]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.2.1
[0.2.0]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.2.0
[0.1.0]: https://gitlab.com/dunn.dev/bairn/-/tags/v0.1.0
