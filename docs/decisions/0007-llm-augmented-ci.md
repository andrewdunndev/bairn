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

The CI integration is operator-side: the manifest and baselines stay
gitignored (operator-only) per the `discovery/PROTOCOL.md` privacy
posture, so flipping `drift_command` to `./bairn drift --diff
discovery/baselines/last` requires the operator to set up a
project-private mechanism to make those artefacts available in the CI
job. For most operators, drift runs locally on a cron and is fine
there.

## Pending follow-on

- **Operator playbook for CI-driven drift** that wires `bairn drift`
  into the `claude-drift-triage` component end-to-end (manifest +
  baseline distribution, scheduled pipeline, archived diff history).
  Lower priority than native drift itself; ships when one operator
  hits the point where local-cron stops being enough.
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
