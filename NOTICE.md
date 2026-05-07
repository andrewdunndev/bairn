# NOTICE

bairn is not affiliated with [Famly][famly]. It is a personal-archive
tool that builds a typed client against Famly's parent-side API
surface (the same surface the official mobile and web apps use) so
that a household can save a local copy of their child's photos and
videos. This document explains the posture under which bairn is
reasonable to use, and the limits of that posture.

[famly]: https://famly.co/

## Why bairn exists

A child accumulates photographic history at a Famly-using nursery
or preschool over years. The vendor surface is the only place that
history lives until a parent extracts it. There is no parent-side
"download my kid's photos" feature in Famly's official client, and
[`jacobbunk/famly-fetch`][jacobbunk] (Python; five years of public
history) is the established prior art. bairn is a more recent
implementation in Go with stricter type safety, in-file metadata
embedding, and optional Immich uploads.

A native parent-side data-export feature has been on Famly's product
backlog for multiple years, requested by school board members and
other integrators, without delivery. Until that feature ships, bairn
provides the export.

[jacobbunk]: https://github.com/jacobbunk/famly-fetch

## Vendored content

`api/immich/openapi.json` is vendored from
[immich-app/immich](https://github.com/immich-app/immich), licensed
under AGPL-3.0. The OpenAPI spec is published openly by the upstream
project; bairn uses it solely to generate a typed client for the
Immich endpoints bairn calls.

## How bairn relates to Famly's terms of use

Famly's [Terms of Use][famly-tou] define the supported interfaces
narrowly. Read strictly, any non-official-client integration is
restricted (sections 9.1(o) and 9.1(t)). In practice:

- When asked directly about parent-side integration, Famly's
  support team directs requesters to inspect the web client's
  network traffic in browser dev tools and construct equivalent
  GraphQL queries. That is documented guidance from Famly support
  for the methodology bairn implements.
- Famly offers an [API access add-on][famly-add-on] for
  organizations that want first-class integration. This is the
  right answer for any non-personal use case (multi-tenant SaaS,
  organizational data exports, write-side workflows). bairn is
  explicitly not that.
- Existing prior art (`jacobbunk/famly-fetch`) has been public for
  five years without enforcement action.

bairn's approach is a personal household tool that respects the
spirit of the platform: one operator, one account, polite request
rates, no shared credentials, no automated re-distribution of
content. The conditions below are the contract.

[famly-tou]: https://www.famly.co/us/terms/terms-of-use-app
[famly-add-on]: https://www.famly.co/

## Conditions for reasonable use

If you intend to use bairn, accept these as part of running it:

1. **Single household, single account.** bairn is not for organizations
   or multi-tenant deployments. The codebase explicitly assumes one
   operator and one set of Famly credentials.
2. **Operator's own credentials only.** Don't run bairn against an
   account you don't own or aren't authorized to act for (a parent
   or co-parent on a household account is fine; an unauthorized
   third party is not).
3. **Single host.** A file lock prevents concurrent runs against the
   same state file from corrupting it. Don't run bairn from multiple
   hosts simultaneously against the same Famly account.
4. **Human-rate access.** bairn's HTTP layer paces requests at a rate
   a single human user would naturally generate. Don't reconfigure it
   to hammer the platform.
5. **No redistribution.** The photos and videos bairn saves describe
   minors and other people's children. Don't redistribute them. The
   in-file metadata (educator name, kid tags, post body) was created
   by Famly users with an expectation of nursery-internal sharing;
   keep it that way.
6. **You accept the risk.** If Famly later objects to your use, you,
   not the bairn maintainers, are responsible for stopping. bairn is
   not a vendor-sanctioned product.

## Privacy

bairn defaults files to mode `0600`. The state file lives under
`$XDG_STATE_HOME` and never travels with the photos. The discovery
toolkit's outputs (captured response shapes, schema dumps, HARs)
are gitignored and stay on the operator's machine.

bairn embeds full Famly-side metadata into each saved file: the
post body, the educator's name, kid-tag names where applicable.
This is the [archival contract](./docs/decisions/0005-archival-posture.md):
context lives with the file. What happens to those files after
the operator has them is the operator's responsibility. If you
plan to share photos beyond your household, strip metadata first
with `exiftool` or similar.

## If you want to integrate at organizational scale

bairn is not the right tool. Talk to Famly directly about their
[API access add-on][famly-add-on]; it's the supported and correct
path for any integrator beyond a single household.

## Reporting

If a Famly representative reads this and wants the project taken
down, wants to open a discussion, or wants the framing amended:
open an issue at gitlab.com/dunn.dev/bairn/-/issues, or contact
andrew@dunn.dev. The project is intentionally small and the
conditions above are intentionally narrow. We're open to a
conversation.
