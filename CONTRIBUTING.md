# Contributing to bairn

bairn is a personal household integration; contributions are welcome
but the bar is "does this make my own cron run more reliably."

## Development environment

- Go 1.25 or newer
- Optional: a Famly account for live testing
- Optional: an Immich instance for end-to-end runs

Tests run without either, against fixture servers.

## The make targets that matter

```
make gen        regenerate api/famly/gen.go and api/immich/imapi/imapi.go
make test       go test -short ./... (recommended for fast iteration)
make smoke      go test -tags=smoke ./internal/... (longer-running)
make lint       golangci-lint run
make build      bin/bairn for the host
make build-linux bin/bairn-linux-amd64 for headless deploy
```

`make gen` requires a populated `discovery/baselines/__schema.json`
for Famly. To produce one, point `discovery/probe/introspect.py` at
the vendor with a valid token. The schema dump is gitignored; each
operator runs discovery on their own credentials. `api/famly/schema.graphql`
is the hand-curated SDL bairn actually validates operations against.

`api/immich/openapi.json` is vendored from
`immich-app/immich:open-api/immich-openapi-specs.json`. Refresh it
periodically; the URL is documented in `api/immich/oapi-codegen.yaml`.

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

1. **Shape probe** (`shape.py`): hits a known endpoint list, emits
   JSON-key signatures, diffs against committed baselines.
2. **Traffic capture** (`capture.md`): drive the official client via
   Playwright, capture HARs, diff against the manifest.
3. **Schema introspection** (`introspect.py`): if the vendor has
   GraphQL with introspection enabled, dump the schema for
   reference and codegen.

Outputs (manifests, baselines, schema dumps, HARs) stay local.
Scripts and protocol are committed.

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
