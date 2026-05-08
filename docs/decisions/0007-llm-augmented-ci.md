# ADR 0007: LLM-augmented CI for drift, summarization, and adversarial input

**Status**: accepted, 2026-05-07

## Context

bairn depends on a vendor surface (Famly) that we don't control.
Drift in the vendor's response shapes will eventually break the
client. The committed shape baselines and `bairn drift` subcommand
detect drift; what they don't do is *interpret* drift, classify it,
or write a clear summary for the maintainer.

Several other CI duties land in the same shape: a human reads a diff
or a log and writes a paragraph saying what changed. Generated-code
review, doc drift, test-failure triage, fuzz input generation. These
are all "small, well-bounded, language-task" reads where a Claude
API call returns more useful output than a regex or grep.

The dunn.dev estate has a CI component catalog at
`gitlab.com/dunn.dev/pipeline`. New CI capabilities land there as
components, not as bairn-specific scripts; the catalog is where
generalization lives.

## Decision

LLM-augmented CI lands as new components in `dunn.dev/pipeline`.
Bairn is the first consumer; other dunn.dev projects pick up the
same components later.

The catalog grows by a small number of focused components, each one
doing a single LLM-driven task:

- **`claude-drift-triage`**: consumes the output of a consumer's
  drift command, asks Claude to classify each change (rename,
  addition, removal, type-change, nullability, breaking) and
  recommend a concrete next step. Optionally opens a GitLab issue
  with the triage in the body. Landed in `pipeline@v1.3.0`.
- **`claude-mr-summary`**: summarizes the diff in generated-code
  paths (`api/*/gen.go` etc.) for MR descriptions. Useful for any
  catalog consumer that uses code generation. Pending.
- **`claude-doc-drift`**: given a README and a list of source files
  defining flags or env vars, asks Claude to flag inconsistencies
  between documented and actual surface. Pending.
- **`claude-fuzz-inputs`**: generates adversarial test inputs for
  sanitization and parsing functions. Outputs to fixtures the
  consumer's test suite reads. Pending.

Each component is a thin shell wrapper around `curl` against the
Anthropic API. No SDK dependency; the catalog's shared CI image
already has `curl`, `jq`, and `python3` available. Inputs and
outputs are JSON.

`ANTHROPIC_API_KEY` lives as a masked, protected CI variable on the
consumer project (or its group). The component reads it from the
job environment without per-component plumbing.

## Privacy and inputs

Input rules per component:

- **Drift triage**: shape signatures only (JSON keys + types, no
  values). No vendor PII can reach the API; baselines and the diff
  are by construction value-less.
- **MR summary**: diffs of generated code, which describe API
  surface, not user data. Safe.
- **Doc drift**: README + flag-defining source files. No vendor
  data.
- **Fuzz inputs**: prompts ask Claude to *generate* adversarial
  inputs; we never send real captured data.

Captured vendor responses, real photo bodies, kid names, and any
account-side metadata stay out of LLM prompts. The bairn discovery
artefacts (`discovery/baselines/__schema.json`,
`discovery/sessions/*`) remain gitignored and operator-only; CI
does not read them for LLM augmentation.

## Considered

- **Anthropic SDK in CI**: pulls in language toolchain, more weight
  for the catalog's CI image. Curl + JSON keeps things minimal.
- **Per-project CI scripts**: bairn-specific shell snippets in each
  consumer. Rejected: the value is reusable; the catalog is the right
  layer.
- **Self-hosted models**: cheaper at high volume, more setup. At our
  expected scale (~10s of CI jobs per month) the API spend is
  negligible (estimated <$5/mo) and the maintenance overhead of a
  self-hosted endpoint isn't worth it.
- **Doing this work directly in bairn**: the components are general;
  other consumers in the catalog will benefit.

## What landed for v0.1.0

- `gitlab.com/dunn.dev/pipeline/claude-drift-triage` template, tagged
  `v1.3.0`. Validated via `glab api ci/lint` against a synthetic
  consumer per the catalog's existing E2E convention.
- `bairn/.gitlab-ci.yml` includes the component on a `drift` stage,
  scoped to `schedule` and `web`-triggered pipelines (not on every
  branch push).
- `ANTHROPIC_API_KEY` configured as a masked, protected CI variable.

The component fires only when its `drift_command` produces non-empty
output. The default bairn input is `echo ''`, a placeholder that
always exits cleanly. No Anthropic API calls happen until that command
starts producing real output, so there is no API spend during default
scheduled runs.

`bairn drift` is native Go as of v0.2.0 (`internal/drift/`): it loads a
TOML manifest, hits each endpoint, computes JSON-key-only signatures,
and optionally diffs against a prior baseline directory. Output is
human-readable lines of the form `path/key: removed`, `path/key: added`,
`path/key: "str" -> "int"`, and the process exits 1 when drift is found.
The Python prototype (`discovery/probe/shape.py`) stays in the repo as
documentation of the methodology and as a reference implementation; the
on-disk signature format is byte-compatible.

## When drift fires

Two distinct triggers, treated differently:

### Tag-push: bairn's own pre-release gate

A `drift-gate` job in bairn's `.gitlab-ci.yml`, scoped to
`if: $CI_COMMIT_TAG`. It builds bairn, runs
`bairn drift --diff discovery/baselines/main` against Famly's
parent-side surface using the operator's own access token, and
fails the tag pipeline when shapes have drifted. This blocks a
broken binary from shipping.

The relationship reasoning works for this trigger:

- It fires only when the maintainer pushes a tag. A human action
  initiates each call; no clockwork.
- Frequency is per release. For bairn that is a few times a year,
  not weekly.
- The intent is verifiable: validate the binary about to ship
  still matches the vendor's responses. Same shape as a smoke
  test against production before deploying. A defensible reason
  for a CI runner to call a vendor.
- Total annual traffic from this trigger is a couple dozen
  requests across a handful of release events, all under the
  operator's own token. Below any abuse-detection threshold and
  visibly tied to releases on the public Releases page.

Operator setup is a single CI variable: `FAMLY_ACCESS_TOKEN` as
masked + protected at the project level. A forker who does not
set it sees the drift-gate job fail at tag time; that is the
intended signal.

### Schedule: deliberately not used against Famly

The catalog `claude-drift-triage` component is included in
bairn's CI on a `drift-triage` stage with rules
`schedule|web`. Its `drift_command` is held at `echo ''`.

Reasoning: a recurring weekly cron firing from a CI runner is
distinguishable from a human user fetching photos. It arrives at
the same hour each week from the same CI infrastructure with no
prior user action. From Famly's logs, that pattern reads as
automated monitoring even when authorized by the operator. For a
small SaaS that engaged with our use case rather than deflecting,
leaning into a posture that looks like surveillance would be
ungenerous.

The component stays included so that any future change of posture
(Famly explicitly inviting a monitoring integration; bairn
outgrowing this household scope) is a one-line `drift_command`
flip, not a re-architecture. Until then, no scheduled pipeline
calls Famly.

The catalog component remains useful for projects whose vendors
have explicitly invited automated monitoring (internal APIs,
vendor-sanctioned monitoring contracts). bairn opts out for
relationship reasons, not technical ones.

## Maintainer workflow

Before tagging:

1. Refresh `discovery/baselines/main/` locally if needed:
   `bairn drift --out-dir discovery/baselines/main`. Inspect the
   `.shape` files; commit if changed.
2. Push the tag. The drift-gate job fires; the maintainer watches
   the pipeline.
3. If clean, package + release stages run; the binary ships.
4. If drift detected, the gate fails; fix the typed client (or
   the manifest if the surface has moved cleanly), update the
   baseline, push a new commit, and re-tag.

## Pending follow-on

- **Operator playbook for CI-driven drift** for a future state
  where Famly (or a different vendor in scope) explicitly invites
  scheduled monitoring. Until then there is no playbook to write;
  bairn's local-maintainer-fired drift is the right shape for the
  current relationship.
- **`claude-mr-summary`** component for generated-code diffs in MR
  descriptions. Most useful once a second catalog consumer adopts
  spec-first codegen.
- **`claude-doc-drift`** component for README-vs-source consistency.
  Lands when the first false-positive forces it.
- **`claude-fuzz-inputs`** component for adversarial inputs. Lower
  priority; only after the first time a sanitizer ships with a hole
  this would have caught.

## Revisit when

- Anthropic API pricing or quota structure changes materially.
- A self-hosted-model setup makes sense for some other reason
  (privacy, latency, etc.) and the catalog can reuse it.
- A second consumer demands a feature the components don't cover
  (e.g. summarization of a different artefact type).
