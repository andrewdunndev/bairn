# Security

bairn is a personal-archive tool. The threat model is small
(single household, single account, operator's own credentials)
and so is the maintainer surface. This document describes how to
report a security issue and what each release ships for
verification.

## Reporting a vulnerability

Email reports to **andrew@dunn.dev** with the subject line
`[SECURITY] bairn`. Include:

- The version of bairn affected (commit SHA or tag).
- A description of the issue and the security impact.
- Steps to reproduce, where applicable.
- Whether you intend to disclose publicly, and any timeline you
  prefer.

I will acknowledge receipt within seven days. From there, the
goal is to either ship a fix or coordinate public disclosure
within thirty days, whichever you and I agree is appropriate.

## In scope

- Credential leakage: state file, log lines, error messages, EXIF
  embedded in saved photos.
- Authentication bypass against the Famly client.
- Code execution via crafted Famly responses, photo bytes, or
  configuration files.
- Race conditions or file-locking flaws that could corrupt the
  state file.
- Pipeline / supply chain issues that would let a malicious build
  pass our release gate (cosign, SBOM, or SLSA verification gaps).

## Out of scope

- The vendor's terms of service. bairn's posture is documented in
  [`NOTICE.md`](NOTICE.md); that is a contract decision, not a
  security issue.
- Issues that depend on running bairn against an account the
  operator does not own. The contract says don't.
- Theoretical issues without a concrete attack path.

## Supply chain

Each tagged release ships, per binary:

- A SHA-256 entry in `checksums.txt`.
- A keyless cosign signature bundle (`<binary>.bundle`) in the
  generic package registry.
- A CycloneDX SBOM (`<binary>.sbom.json`) linked from the Release
  page.
- A SLSA v1.0 provenance attestation
  (`<binary>.sigstore.json`) linked from the Release page.

Verify the binary signature:

    cosign verify-blob \
      --bundle bairn-linux-amd64.bundle \
      --certificate-identity-regexp '^https://gitlab.com/dunn.dev/bairn/.*//\.gitlab-ci\.yml@refs/tags/.+$' \
      --certificate-oidc-issuer https://gitlab.com \
      bairn-linux-amd64

Verify the SLSA provenance:

    cosign verify-blob-attestation \
      --bundle bairn-linux-amd64.sigstore.json \
      --type slsaprovenance1 \
      --certificate-identity-regexp '^https://gitlab.com/dunn.dev/bairn/.*//\.gitlab-ci\.yml@refs/tags/.+$' \
      --certificate-oidc-issuer https://gitlab.com \
      bairn-linux-amd64

If a verification fails, do not run the binary. Open an issue.

Build details, including the catalog components that produce
these artifacts, are in
[`docs/decisions/0007-llm-augmented-ci.md`](docs/decisions/0007-llm-augmented-ci.md)
and in the catalog at
[`gitlab.com/dunn.dev/pipeline`](https://gitlab.com/dunn.dev/pipeline).
