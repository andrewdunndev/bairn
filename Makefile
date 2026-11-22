.PHONY: build build-linux build-darwin build-all test lint clean tidy gen gen-famly smoke-immich pre-tag-check require-exiftool

# The Immich release bairn is verified against via `make pre-tag-check`.
# Renovate tracks it (automerge off). A bump MR means: upgrade the
# home Immich to the new release, then run `make pre-tag-check`.
# renovate: datasource=github-releases depName=immich-app/immich
IMMICH_VERSION := 3.4.2

BINARY := bin/bairn

# Codegen: regenerate the typed Famly client.
# api/famly ← genqlient against api/famly/schema.graphql
gen: gen-famly

gen-famly:
	cd api/famly && go tool genqlient

# Run the full Immich smoke locally: login + mint an ephemeral API key
# holding only fetch's scopes (asset.upload, album.read, album.create,
# albumAsset.create, tag.create, tag.asset) + upload a tiny JPEG, file it
# into unique month/year albums and tag it, twice + assert + clean up
# (asset, albums, tag, key) with the login session.
# SMOKE_SHARE_USER_EMAIL names the user the albums are shared with.
# Useful before tagging if you touched anything in api/immich/ or
# internal/sink/immich. Run it against Immich $(IMMICH_VERSION).
#
# Reads IMMICH_BAIRN_HOST/USER/PASSWORD or falls back to
# IMMICH_BASE_URL + IMMICH_API_KEY.
smoke-immich: build
	$(BINARY) smoke immich

# Stamped into main.Version; "dev" when built without these targets.
VERSION ?= $(or $(shell git describe --tags --always --dirty 2>/dev/null),dev)
LDFLAGS := -X main.Version=$(VERSION)

build:
	mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/bairn

build-linux:
	mkdir -p bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BINARY)-linux-amd64 ./cmd/bairn

build-darwin:
	mkdir -p bin
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w $(LDFLAGS)" \
		-o $(BINARY)-darwin-arm64 ./cmd/bairn

build-all: build-linux build-darwin

test:
	go test -race ./...

# Pre-tag gate. Runs lint, the unit test suite, and the live
# Immich round-trip smoke against IMMICH_VERSION. Treat this as the contract before
# `git tag`: don't cut a release that doesn't pass all three. The
# smoke is the guard that would have caught the v0.4.3 device-
# field regression.
#
# Requires golangci-lint on PATH (mise: `mise install golangci-lint`).
# Requires IMMICH_BAIRN_HOST/USER/PASSWORD (or IMMICH_BASE_URL/
# IMMICH_API_KEY) so the smoke can reach an Immich.
pre-tag-check: require-exiftool lint test smoke-immich
	@echo
	@echo "✓ lint + unit tests + live Immich smoke passed."
	@echo "  Safe to git tag."

require-exiftool:
	@command -v exiftool >/dev/null || { echo "exiftool is required: the metadata round-trip test skips without it"; exit 1; }

lint:
	golangci-lint run

tidy:
	go mod tidy

clean:
	rm -rf bin/ dist/
