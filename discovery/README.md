# discovery/

Probes for verifying the vendor API surface bairn depends on.

The tooling here is deliberately generic: they take a base URL, an
auth header, and an endpoint manifest, then emit shape signatures
that can be diffed against a baseline. Vendor-specific endpoint
manifests, captured baselines, and per-session walks are gitignored
and live on the operator's machine only.

Three modes:

1. **Shape probe** (`bairn drift`): hit a known endpoint list,
   record JSON-key-only signatures. Diff against baseline. Cheap and
   repeatable. Documented in
   [`baselines/main/README.md`](./baselines/main/README.md).
2. **Traffic capture** (`probe/capture.md`): drive the official web
   app via Playwright, capture network. Surfaces endpoints we don't
   yet know about. Manual; outputs are gitignored HAR files.
3. **Schema introspection** (`probe/introspect.py`): probe a GraphQL
   endpoint for `__schema`. Most servers disable introspection; one
   shot to check.

Outputs in this tree are NOT to be committed:

    baselines/
    sessions/
    captures/
    probe/*.json
    probe/*.shape
    probe/*.har

The protocol details live with the operator's private campaign
documentation, not in this repository. `bairn drift` works against any
JSON-over-HTTP surface; what's omitted is the vendor-specific endpoint
manifest. Copy `probe/manifest.example.toml` to `probe/manifest.toml`
(gitignored) and fill in your own.
