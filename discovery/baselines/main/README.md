# discovery/baselines/main/

Shape baseline for the read-side endpoints bairn fetches against,
keys-only with no values. Documents the integration boundary so a
maintainer can run `bairn drift --diff discovery/baselines/main`
before a release and confirm Famly's response shapes still match
bairn's typed clients.

This directory is **committed** to the repo (an exception to the
default operator-only posture for `discovery/baselines/`; see
[`PROTOCOL.md`](../../PROTOCOL.md) for reasoning). Each `.shape`
file represents one endpoint listed in
[`discovery/probe/manifest.toml`](../../probe/manifest.toml).

## Seeding

Every `drift-gate` run keeps its fresh shapes as a one-week job
artifact (`discovery/baselines/current/`). To reseed without local
credentials, run the weekly schedule, download that artifact, check
each value is a type name or `<n=*>`, and commit it here. Prefer a
week whose first posts include an image, a tagged image and a video,
so those subtrees carry real shapes rather than `<empty>`.

Locally, with your own Famly credentials:

```bash
export FAMLY_EMAIL=...
export FAMLY_PASSWORD=...
bairn drift --anonymize --out-dir discovery/baselines/main
```

The resulting `.shape` files contain JSON-key signatures only.
The probe by design strips values, IDs, and PII; `--anonymize`
additionally replaces array length markers `<n=N>` with `<n=*>`
so household-side cardinality (e.g. how many relations the
operator's account has) does not leak into the committed
baseline.

Verify before committing: each file should have only string
sentinels (`"str"`, `"int"`, `"bool"`, `"null"`), `<n=*>`, and
`<empty>`. No literal numbers, no human-readable strings other
than those sentinels.

The drift-gate in `.gitlab-ci.yml` runs `bairn drift --anonymize
... --diff discovery/baselines/main` on each tag, so the probe
shape and the committed baseline shape compare apples-to-apples.
Always re-seed with `--anonymize`; if you re-seed without it, the
next tag's drift-gate reports cardinality drift on every array.

## What stays out of this directory

- Mode 2 outputs (HARs with full bodies). Stay in `discovery/captures/`,
  gitignored.
- Mode 3 outputs (full schema dumps). Stay in
  `discovery/baselines/__schema.json`, gitignored.
- Operator-private probe outputs (additional endpoints the
  operator hits for debugging). Operators stage those under
  `discovery/baselines/<other>/`, which remains gitignored.

## Weekly CI drift check

A scheduled pipeline runs `bairn drift --anonymize --diff` against
this baseline once a week: two GETs, one second apart, on the
operator's own account (ADR 0007). `<empty>` and `null` shapes
match any shape, so a week whose feed happens to carry photos or
rich text does not read as drift. The response filter drops keys
bairn does not declare, so the diff reports removals and type
changes only.
