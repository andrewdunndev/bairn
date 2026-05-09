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

## [0.5.0] - 2026-05-09

This release rebases bairn's CI onto the `dunn.dev/pipeline@2.0.1`
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

- All catalog includes bumped to `@2.0.1`. See
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
  pull `ci-go:2.0.1` (was `:latest`). Pinned tooling matches
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
