# ADR 0003: Famly auth: token-first with optional refresh

**Status**: accepted, 2026-05-07

## Context

Famly issues two flavours of auth credential to a parent account:

- A long-lived UUID-format **session token** retrievable from the
  web app's local storage.
- The user's **email and password**, which can be exchanged via
  the `Authenticate` GraphQL mutation for a fresh session token.

For an interactive run, the token alone is enough. For an
unattended cron, the token will eventually expire mid-run and the
binary needs a way back to a valid session without human input.

## Decision

bairn supports both.

- `FAMLY_ACCESS_TOKEN` (env var or config field) is the primary
  auth credential. Always required.
- `FAMLY_EMAIL` + `FAMLY_PASSWORD` are optional. When present,
  the binary will, on a 401 from any Famly request:
  1. Run the `Authenticate` GraphQL mutation
  2. Cache the new token in memory for the rest of the run
  3. Retry the original request once
  4. If still failing, exit with a prescriptive error pointing the
     operator at credential rotation
- The newly minted token is **not written back to disk**. Cron
  runs are short; the next invocation will look up env again or
  re-run the login. This avoids the "stale token cached on disk"
  failure mode.

## Considered

- **Token-only.** Simplest, but stalls on token expiry. Rejected
  for cron use.
- **Email-and-password only, mint token every run.** Wasteful
  request per run, slower cold starts, hits the auth surface harder
  than necessary. Rejected.
- **Persist refreshed token to config file.** Adds a write path,
  introduces stale-cache bug class, requires careful file locking
  for concurrent runs. Rejected for the simplicity of in-memory.

## Consequences

- Operator can run with token only for short-lived experiments, or
  add credentials for long-running cron.
- Cron with credentials self-heals on token rotation by Famly.
- Cron with token only will stall on rotation; the error message
  tells the operator exactly what to do (set FAMLY_EMAIL and
  FAMLY_PASSWORD).

## Revisit when

- Famly publishes an official long-lived API key surface for
  parents (would replace email-and-password entirely).
- Token expiry frequency makes per-run minting cheap enough to
  drop the in-memory cache complexity.
