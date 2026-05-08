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

The baseline is generated locally by an operator with their own
Famly credentials, then committed. The maintainer regenerates it
before each bairn release to catch breakages early:

```bash
export FAMLY_ACCESS_TOKEN=...
bairn drift --out-dir discovery/baselines/main
```

The resulting `.shape` files contain JSON-key signatures only.
Verify there are no values, no IDs, no PII before committing
(the shape probe by design strips these, but a quick visual check
is cheap).

## What stays out of this directory

- Mode 2 outputs (HARs with full bodies). Stay in `discovery/captures/`,
  gitignored.
- Mode 3 outputs (full schema dumps). Stay in
  `discovery/baselines/__schema.json`, gitignored.
- Operator-private probe outputs (additional endpoints the
  operator hits for debugging). Operators stage those under
  `discovery/baselines/<other>/`, which remains gitignored.

## Why drift is not on a CI schedule

bairn deliberately does not run drift on a recurring CI schedule
against Famly. A weekly cron firing from a CI runner against a
small SaaS would distinguishably read as automated monitoring,
even with the operator's own access token. The maintainer fires
drift by hand before tagging; that's the right level of attention
for the relationship. ADR 0007 documents this choice.
