# testdata/

Synthetic fixtures only. Never commit captured responses from real
accounts.

Fixtures here exist to exercise:

- HTTP plumbing (auth header propagation, retries, exponential
  backoff)
- JSON shape decoding against representative payloads
- EXIF re-injection on known-good JPEG/TIFF samples
- SQLite state transitions (downloaded, uploaded, dedup, retry)
- Immich multipart upload encoding

Schema drift is detected against gitignored baselines in
`discovery/baselines/`, not against the fixtures here. Drift testing
and unit testing are separate concerns: unit tests must remain
runnable in CI without any vendor credentials.

For the Famly API fixtures specifically (sanitization rules, file
inventory, regeneration procedure), see
[`api/famly/testdata/README.md`](../api/famly/testdata/README.md).
