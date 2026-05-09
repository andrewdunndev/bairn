# Thin runtime layer for bairn. Pairs with the v2.0.0 catalog's
# container-image template in `from_artifacts: true` mode: the
# go-release-binary job builds + cosign-signs the binary, this
# Containerfile COPYs it onto ci-runtime-go (UBI micro). The
# binary verifiable via `cosign verify-blob` is byte-for-byte the
# binary inside the container.
#
# No Go toolchain stage. No recompile. Provable parity.
#
# Multi-arch: TARGETARCH is set automatically by the catalog
# container-image template when multi_arch: true. Defaults to amd64
# for single-arch builds. arm64 binaries land in dist/ via
# go-release-binary's parallel:matrix.
#
# Build (via catalog, from a tag pipeline):
#   automatic — go-release-binary -> container-image (from_artifacts)
#
# Run:
#   docker run --rm \
#     -e FAMLY_EMAIL -e FAMLY_PASSWORD \
#     -v ~/Pictures/bairn:/data \
#     registry.gitlab.com/dunn.dev/bairn/cli:latest fetch --max-pages 1

FROM registry.gitlab.com/dunn.dev/pipeline/ci-runtime-go:2.0.2

ARG VERSION=dev
ARG TARGETARCH=amd64

WORKDIR /data
COPY dist/bairn-linux-${TARGETARCH} /bairn

ENV BAIRN_SAVE_DIR=/data \
    BAIRN_STATE_PATH=/data/state.json \
    BAIRN_LOG_FORMAT=json

ENTRYPOINT ["/bairn"]
CMD ["fetch"]

LABEL org.opencontainers.image.source="https://gitlab.com/dunn.dev/bairn" \
      org.opencontainers.image.description="bairn -- personal photo archive for Famly-using households (Go CLI)" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
