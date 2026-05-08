# Multi-stage container build for bairn.
#
# Build:
#   buildah build -t bairn:dev .
# Run:
#   docker run --rm \
#     -e FAMLY_EMAIL -e FAMLY_PASSWORD \
#     -v ~/Pictures/bairn:/data \
#     registry.gitlab.com/dunn.dev/bairn/cli:latest fetch --max-pages 1
#
# State and saves land under /data; mount a host directory there so
# the archive persists across runs. Optional Immich vars
# (IMMICH_BASE_URL, IMMICH_API_KEY) wire the secondary sink.

FROM mirror.gcr.io/library/golang:1.25 AS builder
WORKDIR /src

# Module cache layer first so source-only changes don't refetch deps.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X main.Version=${VERSION}" \
      -o /out/bairn \
      ./cmd/bairn

# Runtime: distroless static. No shell, no package manager, no
# apt-get update CVE noise. Pulled from gcr.io which is not subject
# to the docker.io unauthenticated pull cap that bit other parts of
# this estate.
FROM gcr.io/distroless/static-debian12:latest
WORKDIR /data
COPY --from=builder /out/bairn /bairn

# Container defaults assume /data is the volume mount.
ENV BAIRN_SAVE_DIR=/data \
    BAIRN_STATE_PATH=/data/state.json \
    BAIRN_LOG_FORMAT=json

ENTRYPOINT ["/bairn"]
CMD ["fetch"]

LABEL org.opencontainers.image.source="https://gitlab.com/dunn.dev/bairn"
LABEL org.opencontainers.image.description="bairn -- personal photo archive for Famly-using households (Go CLI)"
LABEL org.opencontainers.image.licenses="MIT"
