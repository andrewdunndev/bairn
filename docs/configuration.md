# Configuration reference

Everything `bairn` reads from the environment, its flags, and the
shapes it prints. The README has the quick start; this is the lookup.

## Configuration

Auth (one of these is required):

| Var | Purpose |
|---|---|
| `FAMLY_EMAIL` + `FAMLY_PASSWORD` | **Recommended.** bairn re-authenticates on token expiry. |
| `FAMLY_ACCESS_TOKEN` | Advanced; use when MFA is enabled on the account. |
| `FAMLY_DEVICE_ID` | Optional UUID pinning the device id sent to Famly. Default: the id stored in the session file, generated once at random. Set it in CI (see below). |

**Session file.** bairn keeps `session.json` next to the state file
(`$XDG_STATE_HOME/bairn/` by default, or the directory of
`BAIRN_STATE_PATH`). It holds two values: the device id Famly knows
this install by, and the last access token. The file is mode `0600`,
written atomically, and the token is never logged. A run starts from
the stored token and logs in with your password only when Famly
rejects it, so Famly's "new log-in activity" e-mail is not sent every
run. A token pasted in `FAMLY_ACCESS_TOKEN` wins over the stored one
for `fetch`, even when credentials are also set; if Famly rejects it,
bairn logs in with the credentials for that run only and writes no
token to the file while the variable is set. `bairn drift` with credentials
always logs in fresh and ignores both. `--dry-run` and `bairn drift`
never write the file. Because they cannot store a device id, a
first-ever `--dry-run` or `drift` on a fresh install (no session file, no
`FAMLY_DEVICE_ID`) that needs a login looks like a new device to Famly;
make the first run on a fresh install a real `fetch`. Delete the file to force a fresh login;
back it up like a credential, not like the archive.

**CI and containers.** A throwaway container has no session file, so
each run would generate a new device id and Famly would report a new
device. Set a fixed `FAMLY_DEVICE_ID` (any UUID you generate once, for
example with `uuidgen`) as a masked CI variable; the drift-gate job,
which runs on tags and weekly schedules, then always presents the same
device and the notices stop. Do the same for a container whose state
directory is not a persistent volume.

Save and state:

| Var | Purpose | Default |
|---|---|---|
| `BAIRN_SAVE_DIR` | Root for saved photos and videos | `$XDG_DATA_HOME/bairn/assets` |
| `BAIRN_STATE_PATH` | JSON state file | `$XDG_STATE_HOME/bairn/state.json` |
| `BAIRN_TZ` | IANA zone (`America/Detroit`) for the date of videos in posts with no zoned image; same as `--tz` | machine's local zone |
| `BAIRN_LOG_FORMAT` | `json` (cron) or `text` (interactive) | `json` |
| `BAIRN_PROGRESS_EVERY` | Log a progress line every N feed pages; `0` turns them off; same as `--progress-every` | `10` |

Optional Immich sink (uploads alongside disk save):

| Var | Purpose | Default |
|---|---|---|
| `IMMICH_BASE_URL` | Immich API URL, ending in `/api`, e.g. `https://photos.example.com/api` | unset |
| `IMMICH_API_KEY` | Immich API key (User Settings → API Keys) | unset |
| `BAIRN_ALBUMS` | `1` files uploads into month and year albums with the default patterns | off |
| `BAIRN_ALBUM_MONTH` | Month album name pattern (`%Y %m %B %b`); setting it enables that album | unset (`EDC %B %Y` with `BAIRN_ALBUMS`) |
| `BAIRN_ALBUM_YEAR` | Year album name pattern; setting it enables that album | unset (`EDC %Y` with `BAIRN_ALBUMS`) |
| `BAIRN_ALBUM_SHARE` | Comma-separated Immich user UUIDs a new album is shared with as editors | unset |
| `BAIRN_TAG` | Immich tag put on every upload; no `/` | `bairn` |
| `BAIRN_NO_TAG` | `1` turns tagging off | off |

**Immich version: v3.0.2 or later.** bairn targets the Immich 3.x
`/assets` upload contract, which no longer takes `deviceId` /
`deviceAssetId`; bairn does not send them, and 2.x servers that still
require them are not supported. It is verified against the
`IMMICH_VERSION` pin in the Makefile (albums and key permissions
included) through `make pre-tag-check`.

Exit codes for `bairn fetch`: 0 means the run finished with nothing
left over. 1 means the run ended but something failed (an item that
could not be downloaded or saved, an upload that failed, an album
operation or tag operation that failed, or a write to the state file that failed, which
includes a failed read of the state file for an item, or a state flush that
fails at close), or a runtime error stopped it; rerun to retry. Under
`--source=tagged` or `--source=liked` a failed `/me` or `/relations` lookup
stops the run before any feed work and exits 1, because a partial household
set would silently drop images. `bairn status` also exits 1 when its state
close fails. An EXIF reinjection failure
is only a warning (the item is saved and uploaded) and still exits 0.
2 means a usage or configuration error. A scheduler's last-success
marker should advance only on 0.

`bairn drift` exits 0 with no drift (or no `--diff`), 1 when drift is
found, and 2 on a configuration or transport error, so a CI job fails
on real changes only. `bairn backfill upload-sha1` exits 0 when every
entry was hashed or skipped, 1 on a hashing error or when the state
cannot be opened (for example while a fetch holds the lock), and 2 on a
usage error.

CLI flag overrides for `bairn fetch`:

```
--max-pages N            stop after N feed pages (default 3, 0 = unlimited)
--tz ZONE                zone for videos in posts with no zoned image
                         (default: the machine's local zone)
--dry-run                enumerate without fetching or saving
--no-immich              save to disk only; skip Immich even when configured
--source MODE            feed filter: all (default; every image and video),
                         tagged (only images tagged with one of your children),
                         or liked (only images liked by a household login)
--save-dir DIR           override BAIRN_SAVE_DIR
--filename-pattern PAT   override the default filename template
--dir-pattern PAT        override the default directory template
--include-system-posts   include automated check-in/sign-out posts
                         (off by default; their templated text isn't great
                         photo caption material)
--albums                 file Immich uploads into month and year albums,
                         using the default patterns below (BAIRN_ALBUMS=1)
--album-month PAT        month album name; setting it enables that album
                         (BAIRN_ALBUM_MONTH; with --albums: "EDC %B %Y",
                         e.g. "EDC November 2026")
--album-year PAT         year album name (BAIRN_ALBUM_YEAR; with --albums:
                         "EDC %Y")
--album-share IDS        comma-separated Immich user IDs (UUIDs) a newly
                         created album is shared with as editors
                         (BAIRN_ALBUM_SHARE)
--tag NAME               Immich tag put on every upload (BAIRN_TAG; default
                         "bairn"; no "/")
--no-tag                 do not tag uploads (BAIRN_NO_TAG=1)
--progress-every N       log a progress line every N feed pages (default 10,
                         BAIRN_PROGRESS_EVERY; 0 = off)
--retry-failed           revisit only state entries that failed to download
                         or save, without walking the whole feed (see below)
--retry-gone             with --retry-failed, revisit entries marked gone
                         (see below)
--allow-missing-state    upload even though the state file is missing and the
                         save dir already holds media (refused by default)
```

Other commands:

| Command | Flag | Meaning |
|---|---|---|
| `bairn login` | `--email` | Famly email (default `FAMLY_EMAIL`) |
| | `--password` | Famly password; insecure, prefer `FAMLY_PASSWORD` or the prompt |
| | `--device-id` | device id sent to Famly (default `FAMLY_DEVICE_ID`, else the session file's) |
| `bairn drift` | `--manifest` | TOML manifest (default `discovery/probe/manifest.toml`) |
| | `--out-dir` | where shape signatures are written (default `discovery/baselines/current`) |
| | `--diff` | prior baseline directory to diff against |
| | `--anonymize` | replace array length markers with `<n=*>` so household cardinality does not leak |
| `bairn smoke immich` | `--host` | Immich base URL (default `IMMICH_BAIRN_HOST`, else `IMMICH_BASE_URL`) |
| | `--user`, `--password` | Immich login for the full smoke (defaults `IMMICH_BAIRN_USER`, `IMMICH_BAIRN_PASSWORD`) |
| | `--api-key` | Immich API key (default `IMMICH_API_KEY`); ignored when user and password are set |
| `bairn backfill upload-sha1` | `--dry-run` | count without writing the state file |
| `bairn repair missing` | `--dry-run` | count without writing the state file |
| `bairn status` | `--missing` | print the ids (state keys only) of saved entries whose file is missing, one per line |

Albums are off unless `--albums` or a pattern is set. Patterns take
`%Y %m %B %b`, read in the run's zone at the post date. An album is
found by exact name among the albums the API key's owner owns and is
created only if absent, so reruns add to the same album. bairn only
adds assets to albums; it never removes an asset, deletes an album or
trashes anything. The Immich API key needs exactly these permissions
and no more: `asset.upload`, for albums `album.read`, `album.create`
and `albumAsset.create`, and for tags `tag.create` and `tag.asset`
(checked against Immich v3.4.2; see "Tags" below for which call needs
which). Share with user IDs rather than emails so the key needs no
`user.read`.

The state file is the tombstone. An asset it records as uploaded is
never uploaded again, even if you later delete it in Immich, so back
the state file up with the archive. An asset Immich refuses for an
album (for example one deleted since upload) is recorded as refused
and skipped from then on. Uploads from before 0.6.0, which carry no
post date, are filed once from the creation time already in the
state file. `bairn status` shows `albumsFiled`, `albumsUnfiled` and
`albumsRefused`. `bairn smoke immich` deletes only its own test
asset, albums and key, through a login session on the smoke account.

### Tags

Every upload, new or answered "duplicate", is put under one Immich tag
(`bairn` by default; `--tag` or `BAIRN_TAG` renames it, `--no-tag` or
`BAIRN_NO_TAG=1` turns it off). The tag is found or created once per
run in the key owner's account and assets are added in batches, so a
rerun adds nothing. Uploads recorded before tagging existed are tagged
once from the state file (`taggedAt`); `bairn status` shows
`tagsApplied`, `tagsOwed` and `tagsRefused`. An asset Immich refuses
(deleted or trashed since upload) is recorded as refused and not
retried; it does not fail the run. A failed tag call does: the asset
stays uploaded, exit is 1, and a rerun tags it. bairn never removes a
tag from an asset or deletes a tag. Changing `--tag` later tags new
uploads only; entries already marked tagged are not revisited.

Rolling out: widen the API key (`tag.create`, `tag.asset`) before the
first fetch at this version, or set `BAIRN_NO_TAG=1` on the timer for
that run, since a key without them makes every fetch exit 1. The first
tagging run also back-files every earlier upload from the state file, in
batches of 100. Rolling back to an older binary drops the new state
fields when it rewrites the file, so rolling forward again re-queues
the tags (harmless, tagging is idempotent) and re-locates undated
failed entries.

The calls and the permission the Immich 3.4.2 spec requires for each
(`x-immich-permission`):

| Call | Permission |
|---|---|
| `PUT /tags` (upsert: create if missing, return the tag) | `tag.create` |
| `PUT /tags/{id}/assets` (add the tag to assets) | `tag.asset` |

`PUT /tags` finds and creates in one idempotent call, so the key needs
no `tag.read`. `tag.asset` also guards the untag call
(`DELETE /tags/{id}/assets`), which bairn never makes; `tag.delete`,
`tag.update` and `tag.read` are not needed and should not be granted.

### State file records

`state.json` is a JSON object keyed by Famly image ID. The record
fields are defined in `internal/state/store.go`; these are the ones
that need a note:

- `sha1`: SHA-1 of the bytes as downloaded, before EXIF reinjection.
- `uploadSha1`: SHA-1 of the bytes sent to Immich, which is Immich's
  asset checksum. It differs from `sha1` for stamped items. Entries
  uploaded before 0.7.1 lack it; `bairn backfill upload-sha1` fills it.
- `feedItemAt`: creation date of the feed item the asset came from,
  the key the feed is ordered by. Entries from before 0.7.0 lack it; a
  fetch stamps it on saved entries it meets, and a retry fills it in.
- `goneAt`: when a `--retry-failed` walk covered the entry's post date
  (or reached the feed's end) and did not find the post. Absent unless
  marked. It is cleared when the entry is saved or fails again.

### Backfilling `uploadSha1`

`bairn backfill upload-sha1 [--dry-run]` is offline: it reads the state
file and the saved files and contacts nothing. For each uploaded entry
with no `uploadSha1` it hashes the saved file, which is the bytes
Immich received, and records the digest. An entry whose EXIF stamp or
file modification time is later than its upload is left blank, since
the file may no longer be what was sent. Existing digests are never
overwritten. The command takes the state lock, so it cannot run beside
a fetch. The summary is JSON:

| Field | Meaning |
|---|---|
| `considered` | uploaded entries with no `uploadSha1` |
| `hashed` | digests recorded (with `--dry-run`, digests that would be) |
| `skippedModified` | left blank because the file changed after upload |
| `missingFile` | no saved path, or the file is gone |
| `errors` | stat, hash or state-write failures; any makes the exit code 1 |

### Recovering saved files that are gone

When saved files are lost (a disk replaced, state moved to a server
without its files), the entries still say saved, so a fetch skips them.
The recovery is three steps: `bairn status --missing` lists them,
`bairn repair missing [--dry-run]` re-queues them, and
`bairn fetch --retry-failed` downloads them again into the current save
root.

`repair missing` is offline and takes the state lock like the backfill.
For every entry with `savedAt` set whose file is absent (the
`status --missing` definition) it clears `savedAt` and `savedPath` and
sets `lastError` to `saved file missing`, all in one state write. It
keeps `immichAssetId`, `uploadSha1`, `feedItemAt` and every other
field. The retry uploads the new file; Immich recognises the checksum
and answers duplicate, so no second asset is made. A file whose stat
fails for a reason other than absence is an error and is left alone.
The summary is JSON:

| Field | Meaning |
|---|---|
| `considered` | entries with `savedAt` set |
| `marked` | entries re-queued (with `--dry-run`, that would be) |
| `present` | entries whose file exists |
| `errors` | stat or state-write failures; any makes the exit code 1 |

Usage errors exit 2.

### Retrying failed items

`bairn fetch --retry-failed` revisits only state entries that have a
`lastError` and no `savedAt`, instead of walking the feed. Famly has no
per-item lookup, so the walk uses the feed's own `olderThan` parameter:
it jumps to the newest failed item's post date (recorded as
`feedItemAt` when the item is discovered) and stops after a page that
ends before the oldest. Entries from before 0.7.0 have no `feedItemAt`;
the walk starts at their discovery time (a post cannot be newer than
the run that first saw it) and, with no known oldest date, stops only
once every entry is found, so `--max-pages 0` is its default (`--max-pages N`
still caps it). If the server ignored `olderThan`, the walk simply
starts at the newest post and still stops at the same place. Entries
found are filled in with their post date for next time.

An undated entry whose post has since vanished from Famly cannot be
found, so with no stop date the walk reads the whole feed to be sure.
Bound that with `--max-pages N`, run `--retry-failed --dry-run` first to
see the date it would jump to, or accept the full walk once; entries it
does find are dated afterwards, and the entries it does not are reported
as `notFound`.

A walk that did not find an entry and covered it (it read past the
entry's post date, or to the feed's end) marks the entry `goneAt`, and
later `--retry-failed` runs skip it (the mark is cleared when the entry is saved or fails again), so a deleted post costs one walk,
not one per run. A walk stopped by `--max-pages` marks only the entries
whose post date it had already read past; an error marks nothing, and
`--dry-run` never marks. The summary's `gone` counts the
entries marked this run. `--retry-failed --retry-gone` revisits the
marked entries too: one found is filled in and unmarked, one still
missing is marked again. `--retry-gone` without `--retry-failed` is refused (exit 2); a
run that skips marked entries logs "skipping entries marked gone".

The flag cannot be combined with `--source`, `--include-system-posts`
or `--allow-missing-state`, works with `--dry-run`, and skips the album
and tag back-fill. The run summary gains a `retry` block: `failed`
(entries revisited), `retried` (found in the feed and run again),
`fixed` (now saved and, with Immich on, uploaded), `uploadFailed`
(saved, but the upload failed; the next fetch uploads it),
`stillFailing` (not saved: failed again or not found), `notFound` (the post is gone, the video is still transcoding, or
`--max-pages` ended the walk first), `undated` (entries with no
`feedItemAt`, located from their discovery time) and `gone` (entries
marked `goneAt` this run; they are also counted in `notFound` and
`stillFailing`). `uploadFailed`, `undated` and `gone` are left out when
zero. A new failure exits 1; an entry that is simply not found does
not. Under `--dry-run` nothing is fixed or marked, so `fixed` and
`stillFailing` print 0 and `uploadFailed` and `gone` are left out.

### Run summary

The summary printed at the end is JSON. `discovered` counts every media
item read from the feed. `buckets` places each in exactly one outcome,
so the buckets add up to `discovered`; a mismatch is logged as an error
and shown as `unreconciled`:

| Bucket | Meaning |
|---|---|
| `savedNew` | fetched and saved this run, Immich off |
| `alreadyDone` | saved (and uploaded) by an earlier run |
| `uploadedNew` | confirmed in Immich as newly created |
| `uploadedDuplicate` | Immich answered "duplicate" |
| `skippedSystem` | on a system post, without `--include-system-posts` |
| `filteredBySource` | excluded by `--source` |
| `notReady` | a video still transcoding |
| `wouldProcess` | a `--dry-run` item a real run would act on |
| `failed` | could not be fetched, saved, uploaded or recorded; rerun retries |

The older counters stay and count different things, which is why they
never added up: `saved` is files newly written to disk this run (not
disk-level duplicates, which are `duplicates`), `uploaded` is only
Immich "created" answers, and `uploadDuplicates` the "duplicate" ones.
A run where `saved` exceeds `uploaded` by a few is therefore normal
(uploads answered "duplicate", or saved items whose upload failed);
`buckets` is the reconciled view. Downloads that fail with a transient
error (connection reset, truncated body, timeout, 5xx) are retried in
the run, up to four tries with backoff, each with a fresh signed URL.
`--progress-every N` logs a `progress` line every N pages with the date
of the last post read and the running counts.

To reconcile a fetch: `discovered` = `alreadyDone` + new (`savedNew` +
`uploadedNew` + `uploadedDuplicate`) + `notReady` + `failed` + the
other buckets (`skippedSystem`, `filteredBySource`, `wouldProcess`).
`stamped` (left out when 0) counts the `feedItemAt` stamps written this
run onto entries saved before the field existed; it is a side effect
inside `alreadyDone`, not a bucket, and a dry run writes none.

To reconcile `bairn backfill upload-sha1`: `considered` is the entries
uploaded with an empty `uploadSha1`. Entries the same day's fetch
uploaded with the checksum are not in it, so `considered` is smaller
than the count of `alreadyDone` entries. Each considered entry lands in
`hashed`, `skippedModified`, `missingFile` or `errors`.

`bairn status` adds `undated` (saved entries with no `feedItemAt`) and
`missingFile` (entries with `savedAt` set whose `savedPath` is not a
regular file). It stats files and never hashes them. `bairn status
--missing` prints the ids of the `missingFile` entries instead of the
summary.


## Filename layout

Default: `{{.Source}}-%Y-%m-%d_%H-%M-%S-{{.ID}}.{{.Ext}}` under
`%Y-%m-%d/`. So a real saved file looks like
`2026-05-06/feed-image-2026-05-06_18-33-21-<uuid>.jpg`. Tokens:

- Strftime: `%Y %m %d %H %M %S %j` (UTC, from the asset's vendor-side
  timestamp).
- Go template: `{{.Source}} {{.ID}} {{.FeedItemID}} {{.Ext}}`.

Override `--filename-pattern` and `--dir-pattern` to match your own
layout convention.

## Embedded metadata, per file

bairn writes both EXIF and XMP, on the principle that some readers
prefer one and some prefer the other (see [ADR 0005](./decisions/0005-archival-posture.md)).
Concretely:

| Tag | Source | Notes |
|---|---|---|
| EXIF `DateTimeOriginal` | `image.createdAt.date` | Famly's timestamp for the image. |
| EXIF `OffsetTimeOriginal` | `image.createdAt.timezone` | Read from Famly; commonly UTC. |
| EXIF `ImageDescription` | post body, sanitized | Newlines collapsed; truncated at word boundary. |
| EXIF `Artist` / XMP `dc:creator` | educator name | Whoever posted. |
| EXIF `Software` | bairn version | Self-attribution. |
| EXIF `UserComment` | full body + sender | Unicode-safe; some viewers warn on dsoprea's "Unicode" prefix. |
| XMP `dc:description` | full body | Newlines preserved; XMP can carry them safely. |
| XMP `dc:subject`, `digiKam:TagsList` | per-image kid tag names (flat; Immich reads TagsList) | When Famly tags the photo per child. |
| XMP `photoshop:DateCreated` | image timestamp | ISO 8601 with offset. |

A video carries no zone, so its sidecar date borrows the zone of a
zoned image in the same post. A post with no zoned image uses `--tz` /
`BAIRN_TZ`, else the zone of the machine running bairn, with the
offset computed at the post's date (daylight saving included). Run
the backfill on a machine whose clock is in the family's zone, or set
`--tz`. The zone in use is logged at the start of every fetch.

GPS coordinates are off by default. They embed only when the
operator supplies coordinates explicitly.
