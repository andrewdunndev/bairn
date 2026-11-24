![hero](hero.svg)

# bairn

[![license](https://img.shields.io/badge/license-MIT-green)](LICENSE)
![Go](https://img.shields.io/badge/lang-Go_1.26+-00ADD8?logo=go&logoColor=white)
[![release](https://gitlab.com/dunn.dev/bairn/-/badges/release.svg)](https://gitlab.com/dunn.dev/bairn/-/releases)
![Built with GitLab](https://img.shields.io/badge/built_with-GitLab-FC6D26?logo=gitlab)

A personal archive tool for [Famly][famly]-using households. bairn
pulls your kid's photos and videos from Famly's nursery feed and
saves them to a directory on your own disk, with full metadata
(timestamps, post body, educator name, kid tags) embedded into each
file via [EXIF][exif] and [XMP][xmp]. Nothing about your child's
photo history depends on Famly continuing to exist as a company.

Bairn is an [archival][archival] tool, not a privacy gateway: every
piece of context the vendor exposes lands inside the file itself.
What happens to those files after you have them is your call.

[famly]: https://famly.co/
[exif]: https://en.wikipedia.org/wiki/Exif
[xmp]: https://en.wikipedia.org/wiki/Extensible_Metadata_Platform
[archival]: ./docs/decisions/0005-archival-posture.md

## Who this is for

You're a parent (or co-parent, grandparent, household admin) whose
kid's daycare or preschool uses Famly, and you:

- Want a [permanent local copy][permanent-local] of every photo and
  video the educators post, in date-organized folders that survive
  Famly outages, account closures, and corporate reshuffles.
- Want the metadata (post body, educator's name, the day the photo
  was taken) carried *inside* the file via standard EXIF/XMP, so any
  photo viewer or future archival workflow can read it.
- Are comfortable running a single Go binary from a terminal or
  cron, on a laptop or a small home server.

If your school doesn't use Famly, or if you're already happy with
how Famly's mobile app surfaces history, bairn is not for you.

[permanent-local]: https://en.wikipedia.org/wiki/3-2-1_backup_strategy

## What it does

![pipeline flow](diagrams/pipeline-flow.svg)

bairn does five jobs:

1. **Authenticate.** You give bairn your Famly email and password
   once via `bairn login`; it exchanges them for a session token at
   the Famly GraphQL endpoint and verifies the token works for API
   calls. For unattended runs, set `FAMLY_EMAIL` and `FAMLY_PASSWORD`
   in your shell or cron environment so bairn re-authenticates when
   the session token rotates. A static `FAMLY_ACCESS_TOKEN` is also
   accepted for [MFA-protected accounts][mfa-fallback].

2. **Paginate the feed.** bairn walks Famly's
   `/api/feed/feed/feed` endpoint, page by page, stopping at
   `--max-pages` (default 3; `0` walks the whole feed, which a
   full-history backfill needs) or when Famly returns no more items.
   The default `--source=all` mode includes every image and video on
   each post; alternative `--source=tagged` and `--source=liked`
   filters are available for households whose schools tag photos per
   kid.

3. **Fetch the highest-resolution variant.** Famly's CDN serves
   sized variants under path segments like `/1024x768/`; bairn
   rewrites the segment to the image's reported original
   dimensions, so what lands on disk is the largest JPEG the server
   stored, not the medium thumbnail.

4. **Embed metadata in-file, then save atomically.** Each saved
   JPEG carries the post body, the post timestamp, the educator's
   name, and any per-image kid-tags as both [EXIF][exif] (legacy +
   ubiquitous) and [XMP][xmp] (modern, what Apple Photos and
   Lightroom prefer). Photos need no sidecar; everything travels with
   the image. Videos get a `<file>.xmp` beside them (description, date,
   tags), which Immich also receives as `sidecarData`. See [ADR 0005][adr-0005] for the archival contract.

5. **Track progress in a JSON state file.** A single
   `state.json` records what's been downloaded, saved, and
   (optionally) uploaded. Reruns skip work already done, and upload
   any saved asset Immich has not confirmed (uploaded or duplicate)
   from the disk sink, including its video `.xmp` sidecar, so a failed
   or skipped upload is fixed by rerunning `bairn fetch`. The run
   summary counts `saved`, `uploaded`, `uploadDuplicates` and
   `uploadFailed`, and a `buckets` block that places every discovered
   item in exactly one outcome (see the [run summary](./docs/configuration.md#run-summary)). A 401 or 403 from
   Immich, or five failed uploads in a row, stops uploads for the rest
   of the run: the remaining assets are still saved to disk and counted
   in `uploadFailed`, and a rerun uploads them. State
   is flushed every 50 changes and on exit, so a hard kill redoes at
   most that many (disk and Immich checksum dedupe absorb the repeat).
   The state file lives under [`$XDG_STATE_HOME`][xdg] by default and is held
   under an OS file lock so concurrent runs fail fast rather than
   corrupt state.

[mfa-fallback]: ./docs/decisions/0003-auth-token-and-refresh.md
[adr-0005]: ./docs/decisions/0005-archival-posture.md
[xdg]: https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html

## End-to-end setup

What you'll need:

- A [Famly][famly] account (your normal parent login).
- A laptop or small server that can run a Go 1.26+ binary.
- Under ten minutes for first-time setup. Subsequent runs are
  `bairn fetch`.

```
# 1. Build the binary.
git clone https://gitlab.com/dunn.dev/bairn.git
cd bairn
make build                                     # produces bin/bairn
sudo install bin/bairn /usr/local/bin/         # optional

# 2. Verify your Famly credentials work.
bairn login                                    # interactive, no-echo
# login: ok (logged in as you@example.com, N children visible)

# 3. Make those credentials available to fetch runs.
export FAMLY_EMAIL=you@example.com
export FAMLY_PASSWORD=<your-password>

# 4. Pull a small first batch.
bairn fetch --max-pages 1
bairn status
ls $XDG_DATA_HOME/bairn/assets/                # date subfolder; full-res JPEGs
bairn --version                                # also: bairn version
```

For unattended runs, put the env vars in a file (mode 0600) and
source it from your crontab or systemd unit.

### Or run the container

Each tagged release ships a minimal runtime OCI image (amd64; arm64 hosts use the signed
binary) at
`registry.gitlab.com/dunn.dev/bairn/cli` (`:latest` and `:vX.Y.Z`):

```
docker pull registry.gitlab.com/dunn.dev/bairn/cli:latest

docker run --rm \
  -e FAMLY_EMAIL -e FAMLY_PASSWORD \
  -v ~/Pictures/bairn:/data \
  registry.gitlab.com/dunn.dev/bairn/cli:latest \
  fetch --max-pages 1
```

The image's working directory is `/data`; saved photos and the JSON
state file land there. Mount a host directory so the archive
persists between runs. Optional Immich vars (`IMMICH_BASE_URL`,
`IMMICH_API_KEY`) wire the secondary sink as in the binary case.

## Reference

Configuration, flags, exit codes, tags, retrying failed items, the run
summary, the state file, filename layout and embedded metadata are in
[docs/configuration.md](./docs/configuration.md). The short version:

- Auth is `FAMLY_EMAIL` and `FAMLY_PASSWORD`, or `FAMLY_ACCESS_TOKEN`
  for an MFA account. Immich is optional (`IMMICH_BASE_URL`,
  `IMMICH_API_KEY`, Immich v3.0.2 or later).
- `bairn fetch` exits 0 only when nothing was left over, 1 when
  something failed (rerun to retry), 2 on a usage error. Advance a
  scheduler's last-success marker on 0 only.
- `bairn fetch --retry-failed` revisits only the items that failed.
- `bairn backfill upload-sha1` fills in the Immich checksum on older
  state entries, offline.
- `bairn verify [--dry-run]` asks a sink that can confirm copies
  (Immich is the one today) whether it holds each uploaded entry's
  bytes and stamps the result in state; it deletes nothing. With no
  such sink configured, `verify` and `prune` exit nonzero.
- `bairn prune [--apply]` deletes the local copy of entries Immich is
  verified to hold. It is a dry run unless `--apply`, and the real prune
  is operator-gated: run it only after a restore drill, and run `verify`
  then a `prune` dry run first. See `docs/configuration.md`.
- `bairn repair pruned [--dry-run] [ID...]` undoes a prune by Famly
  re-download: it re-queues pruned entries, then `bairn fetch
  --retry-failed` saves the files again. Nothing is uploaded twice, since
  each entry keeps its Immich receipt.
- The state file is the tombstone for uploads: back it up with the archive.

## Developing

`make test`, `make lint` and `make build` are the daily loop.
[CONTRIBUTING.md](./CONTRIBUTING.md) has local dev, the pre-tag gate and
the live Immich smoke test.

## Discovery and drift

bairn talks to a vendor surface we don't control. The
[discovery toolkit](./discovery/PROTOCOL.md) is bairn's
methodology for noticing when Famly's response shapes change. Three
modes:

- **Shape probe** (`bairn drift`): hit a known endpoint list, record
  JSON-key signatures, diff against committed baselines; see
  [`discovery/baselines/main/README.md`](./discovery/baselines/main/README.md).
- **Traffic capture**: drive Famly's web app via Playwright,
  capture HARs, find endpoints we don't yet know about.
- **Schema introspection**: when a vendor exposes GraphQL with
  introspection enabled, dump the schema for typed code gen.

Captures, HARs, schema dumps and local manifests stay untracked on
purpose. The canonical `discovery/probe/manifest.toml` and the
anonymized `discovery/baselines/main/` shapes are the exception: they
are committed because the CI drift gate reads them.

## Privacy posture

Discovery captures, HAR files, schema dumps, local manifests and the
generated state file are gitignored. The committed source tree
carries the methodology, plus the canonical endpoint manifest and
the anonymized `main` baseline, which the CI drift gate needs.

The [discovery toolkit](./discovery/) is generic. Operators provide
their own manifest (copy from `discovery/probe/manifest.example.toml`
to the ignored `discovery/probe/manifest.local.toml`) and run probes against their own
credentials.

Files saved by bairn default to mode `0600` because the photos and
their embedded metadata describe minors. The operator can loosen
explicitly. Bairn embeds every piece of context Famly exposes into
the file's metadata; what travels with the file when the operator
moves it is the operator's call.

## Status

The disk-first save path is validated against the maintainer's Famly
account, with full EXIF and XMP embedded and reruns idempotent; other
accounts are not yet covered. The Immich path is live-tested against
the `IMMICH_VERSION` pin through `make pre-tag-check` before each
tag. A weekly drift check compares Famly's response shapes against
committed baselines. GitHub releases on the mirror carry notes only;
signed binaries, SBOMs and provenance live on the GitLab release.

## Architecture

The non-obvious design decisions are written up under
[`docs/decisions/`](./docs/decisions/). If you read one
supplementary file beyond this README, make it
[ARCHITECTURE.md](./ARCHITECTURE.md).

The seven ADRs:

- [0001](./docs/decisions/0001-spec-first-clients.md) spec-first typed clients via codegen
- [0002](./docs/decisions/0002-typestate-asset-lifecycle.md) typestate for the asset lifecycle
- [0003](./docs/decisions/0003-auth-token-and-refresh.md) Famly auth: token-first with optional refresh
- [0004](./docs/decisions/0004-state-as-json-file.md) state as a JSON file with file lock
- [0005](./docs/decisions/0005-archival-posture.md) archival posture, in-file metadata (video XMP sidecar only)
- [0006](./docs/decisions/0006-sink-abstraction.md) sink abstraction (disk + Immich + future)
- [0007](./docs/decisions/0007-llm-augmented-ci.md) drift gate in CI (LLM triage removed)

## Prior art

- [`jacobbunk/famly-fetch`][jacobbunk] (Python). Built an integration
  against Famly's API first; bairn's REST surface follows that map.
  Different design (streamed downloads, JSON state, optional Immich
  layer, in-file photo metadata, XMP sidecar for video only) and different language
  but same conceptual lineage. Established prior art; see
  [`NOTICE.md`](./NOTICE.md) for context.

[jacobbunk]: https://github.com/jacobbunk/famly-fetch

## On the integration path

bairn implements Famly's parent-side API surface using the operator's
own credentials, against the same endpoints the official mobile and
web apps use. It is the single-household, personal-archive expression
of that path: one operator, one account, one host, human-rate access
(requests paced at the rate a single human user would naturally
generate; see condition 4 in `NOTICE.md`). See [`NOTICE.md`](./NOTICE.md)
for the integration-path detail and the full conditions under which
using bairn is reasonable.

A parent-side data-export feature has been on Famly's product
backlog since 2024 without delivery. Until that feature ships,
bairn provides a local archive so that a kid's photo history isn't
indefinitely contingent on Famly continuing to host it.

If your organization needs richer integration than bairn (multi-
account, push notifications, write access, write-back to Famly),
the official API access add-on is the right answer. bairn is
explicitly *not* that.

## License

[MIT](./LICENSE).

bairn is not affiliated with Famly. Use only with credentials for an
account you own. Respect rate limits like a single human user would.
This is a personal-archive tool: single household, single account,
single host. See [`NOTICE.md`](./NOTICE.md) for full context.
