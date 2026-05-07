# ADR 0004: state as a JSON file with file lock

**Status**: accepted, 2026-05-07

## Context

bairn tracks per-asset progress across runs (which Famly image IDs
have been downloaded, EXIF-fixed, saved, optionally uploaded). The
original implementation used SQLite via modernc.org/sqlite and a
sqlc-shaped query layer.

For our scale (a household, thousands of rows lifetime) and
operational shape (one cron run at a time, single user, single
host) SQLite is overkill on three axes:

1. **Binary weight.** modernc.org/sqlite contributes ~9 MB of the
   14 MB binary. Roughly two-thirds of bairn's static binary is a
   SQLite engine we never use the full power of.
2. **Schema migrations.** Adding a column requires a migrations
   table and a migration runner. For a single-author, single-host
   tool, that's machinery without a use case.
3. **Concurrent writers.** SQLite's WAL is well-tuned for them;
   bairn doesn't have any. Cron uses flock to serialise; concurrent
   bairn processes are misuse, not a supported mode.

A JSON state file with atomic write and an OS-level file lock
covers the same surface at a fraction of the size, and is debuggable
with `cat` and `jq`.

## Decision

State persists as a single JSON file. Default location:
`$XDG_STATE_HOME/bairn/state.json`. Operators can override via
`BAIRN_STATE_PATH` env or `--state-path` flag; a common alternate
is `<save-dir>/.bairn-state.json` for keep-photos-and-state-together
deployments.

Format: a flat object keyed by Famly image ID, values are records:

```json
{
  "<famly-image-id>": {
    "source": "feed-image",
    "feedItemId": "...",
    "discoveredAt": "2026-05-06T14:00:00Z",
    "downloadedAt": "...",
    "savedAt": "...",
    "savedPath": "feed-image-2026-05-06_14-00-00-<id>.jpg",
    "sha1": "...",
    "exifFixedAt": "...",
    "exifError": null,
    "uploadedAt": null,
    "immichAssetId": null,
    "immichStatus": null,
    "lastError": null,
    "retries": 0
  }
}
```

Concurrency: an OS file lock (`syscall.Flock` LOCK_EX|LOCK_NB on
unix; `LockFileEx` on Windows) is held for the duration of a run.
A second `bairn fetch` against the same state file fails fast with
a prescriptive message naming the lock holder if discoverable.

Atomic update: write to `<state-path>.tmp` then `os.Rename`. Survives
crash mid-write; the prior file remains intact.

Schema evolution: the JSON record is a Go struct with `omitempty`
tags. Adding a new optional field is a no-op for existing files;
removing a field is a `json` tag rename plus a one-shot loader that
copies forward. No migrations machinery; explicit code per change.

## Considered

- **SQLite via modernc.org/sqlite + sqlc.** The original cut.
  Worked. Heavyweight for the scale; binary cost is the dominant
  factor.
- **YAML.** Whitespace fragility for machine-managed files. Rejected.
- **BoltDB / bbolt.** Embedded KV store. Smaller than SQLite. Loses
  the "I can `cat` it" debugging affordance.
- **Append-only log file.** Smaller writes; needs compaction logic.
  Rejected as not worth the complexity at this scale.

## Consequences

- Binary shrinks measurably (~13 MB for the unstripped host build
  after the SQLite removal; -ldflags strip trims further).
- State is human-readable; debugging is `jq '.[]|select(.uploadedAt==null)'`.
- Stats become an in-memory pass over the loaded map.
- Concurrent runs fail fast and obvious; previously they would
  serialise behind WAL with mysterious timing.
- Adding fields is "just add to the struct"; removing is "ship a
  loader that drops the old key during decode."

## Revisit when

- The household state grows past ~100k assets and JSON load times
  start showing up in startup latency.
- Multi-host deployment becomes a real use case (it currently
  isn't).
- Schema changes start happening monthly; at that point a real
  migration framework earns its weight.
