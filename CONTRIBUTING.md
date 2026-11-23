# Contributing to bairn

bairn is a personal household integration; contributions are welcome
but the bar is "does this make my own cron run more reliably."

## Development environment

- Go 1.26 or newer
- Optional: a Famly account for live testing
- Optional: an Immich instance for end-to-end runs

Tests run without either, against fixture servers. Never put a real
token, password, device id or family data in a test, fixture or commit.

The Famly session (device id and token) lives in `session.json` next
to the state file; see `internal/session`. Tests that touch it use a
temp dir and a fake Famly server, never the operator's real file.

## The make targets that matter

```
make gen        regenerate api/famly/gen.go
make test       go test -race ./...
make lint       golangci-lint run
make build      bin/bairn for the host
make build-linux bin/bairn-linux-amd64 for headless deploy
make build-darwin bin/bairn-darwin-arm64 for an Apple Silicon laptop
make build-all   both of the above
make smoke-immich live round-trip against your Immich
make pre-tag-check lint + test + smoke-immich (run before git tag)
```

`make gen` reads `api/famly/schema.graphql`, the hand-curated SDL
bairn validates operations against; introspection
(`discovery/probe/introspect.py`) is an optional discovery aid.

The Immich client in `api/immich` is hand-written. `IMMICH_VERSION`
in the Makefile is the Immich release it is verified against via
`make pre-tag-check`; Renovate bumps it, and a bump MR means upgrade
the home Immich, then run `make pre-tag-check`.

## Release gates

A tag is cut only on a commit whose main pipeline is green.
`govulncheck` (`audit`) is advisory, so a vulnerability-database
change cannot block a release; `osv-verdict` is the blocking
dependency gate on main. `osv-verdict`, `reference-check` and the site
build do not run on tag pipelines, and `drift-gate` runs only on tags
and the weekly schedule. The drift-gate job runs in a throwaway container with no session
file, so give it a fixed masked `FAMLY_DEVICE_ID` project variable;
otherwise every tag and weekly run is a new device to Famly. Nothing
in CI reaches Immich. `stamp-check` (tag only) flags a
`+dirty` binary stamp after the release is published, so a red run
means yank the release and re-tag.

`make pre-tag-check` on a machine with `exiftool` installed is the
only place the reader-side metadata round trip and the live Immich
contract are exercised; CI covers neither.

The EXIF and GraphQL libraries are feature-complete upstream and
flagged abandoned by Renovate; they are kept because the exiftool
round-trip test pins their behaviour.

## Pre-tag gate

Before cutting a release tag, run `make pre-tag-check`. It runs
golangci-lint, the unit suite, and a real-server round trip against
your Immich: login, mint an ephemeral API key, upload a tiny JPEG
through the production sink, assert created, delete the asset and the
key. The round trip catches controller-layer wire-contract changes no
static spec models. v0.4.3 shipped without it and broke uploads against
Immich v2.7.5.

`make smoke-immich` reads `IMMICH_BAIRN_HOST`, `IMMICH_BAIRN_USER` and
`IMMICH_BAIRN_PASSWORD` (a quota-limited test user, separate from your
archive account), or falls back to `IMMICH_BASE_URL` and
`IMMICH_API_KEY` for ad-hoc runs. It is a local gate only: CI runners
cannot reach a LAN Immich.

On the login path the smoke also runs the album and tag legs. It mints
a key with only `asset.upload`, `album.read`, `album.create`,
`albumAsset.create`, `tag.create` and `tag.asset`, uploads a tiny asset
and files it into uniquely named `bairn-smoke-*` month and year albums
twice through the filer fetch uses, and asserts one album per name and
the asset in each (with `SMOKE_SHARE_USER_EMAIL` set, that user as
editor). It then tags the asset through the same tagger and asserts the
tag was created once, the asset carries it, a rerun adds nothing, and a
missing asset comes back as a refusal. The login session deletes the
albums, tag, asset and key, even on failure.

`make test` includes a metadata round-trip that reads back what bairn
wrote with `exiftool -j`. It skips when `exiftool` is not on PATH, so a
bare CI stays green, but `make pre-tag-check` fails without it. Install
with `brew install exiftool` or `apt install libimage-exiftool-perl`.

## Code conventions

- Generated code is read-only. Don't edit `gen.go` files; rerun
  `make gen` instead.
- Vendor-facing knowledge (URLs, GraphQL ops, JSON shapes) belongs
  in `api/`, not `internal/`.
- Errors should be prescriptive: failure messages should tell the
  operator what to do next, not just what went wrong. See ADR 0003
  for the shape of one canonical example.
- `log/slog` for structured logging. Default JSON; text is
  selectable via `BAIRN_LOG_FORMAT=text` for interactive runs.
- Tests use `httptest.Server` against fixtures; never hit live
  vendors in default `make test`. Drift detection is the explicit
  exception, behind `bairn drift`.

## Discovery and drift

When the vendor's surface drifts, bairn breaks. The `discovery/`
tree is the toolkit for noticing.

`discovery/PROTOCOL.md` documents the methodology; `discovery/probe/`
is the generic-by-construction implementation. Run modes:

1. **Shape probe** (`bairn drift`): hits a known endpoint list, emits
   JSON-key signatures, diffs against committed baselines; see
   [`discovery/baselines/main/README.md`](./discovery/baselines/main/README.md).
2. **Traffic capture** (`capture.md`): drive the official client via
   Playwright, capture HARs, diff against the manifest.
3. **Schema introspection** (`introspect.py`): if the vendor has
   GraphQL with introspection enabled, dump the schema for
   reference and codegen.

Captures, HARs, schema dumps and local manifests stay untracked
(`manifest.local.toml` is the ignored name). The canonical
`discovery/probe/manifest.toml` and the anonymized
`discovery/baselines/main/` shapes are committed on purpose, because
the CI drift gate reads them. Scripts and protocol are committed.

## Design principles

bairn applies four design principles:

- **Derived obligations**: typed clients are codegen, not handwritten.
- **Bundled enforcement**: typestate makes the asset lifecycle
  uncircumventable at compile time.
- **Prescriptive failure**: errors say what to do next.
- **Vacuity detection**: tests assert non-zero results, not just
  "no error returned."

If a contribution moves bairn away from any of these, an ADR
explaining why is welcome.
