# Immich shape baseline

This directory holds the operator's seeded shape signatures for the
Immich endpoints in `discovery/probe/manifest-immich.toml`.

Empty until the operator runs:

```
export IMMICH_BASE_URL=https://photos.example.com
export IMMICH_API_KEY=...
bairn drift --anonymize \
  --manifest discovery/probe/manifest-immich.toml \
  --out-dir discovery/baselines/main-immich
```

The CI job `drift-gate-immich` consumes this baseline for tag
pipelines. With no signatures here, the gate is a soft passthrough
(no comparison data → no drift detected). With signatures in place,
shape changes in `/api/server/version` or `/api/users/me` fail the
tag pipeline before a binary that expects the prior shape ships.

This baseline is operator-private (each Immich instance has its own
shape footprint). Do not assume one operator's seeded files are
correct for another.
