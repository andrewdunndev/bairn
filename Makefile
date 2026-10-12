.PHONY: build build-linux build-darwin build-all test smoke lint clean tidy gen gen-famly refresh-immich-validator smoke-immich pre-tag-check

# The Immich release bairn is verified against via `make pre-tag-check`.
# Renovate tracks it (automerge off). A bump MR means: upgrade the
# home Immich to the new release, recapture api/immich/required-fields.json
# with `make refresh-immich-validator`, then run `make pre-tag-check`.
# renovate: datasource=github-releases depName=immich-app/immich
IMMICH_VERSION := 3.2.4

BINARY := bin/bairn

# Codegen: regenerate the typed Famly client.
# api/famly ← genqlient against api/famly/schema.graphql
gen: gen-famly

gen-famly:
	cd api/famly && go tool genqlient

# Run the full Immich smoke locally: login + mint ephemeral API
# key + upload tiny JPEG + assert + delete asset + delete API key.
# Useful before tagging if you touched anything in api/immich/ or
# internal/sink/immich. Run it against Immich $(IMMICH_VERSION).
#
# Reads IMMICH_BAIRN_HOST/USER/PASSWORD or falls back to
# IMMICH_BASE_URL + IMMICH_API_KEY.
smoke-immich: build
	$(BINARY) smoke immich

# Validator-probe-only mode (no upload). Captures the required-
# field set into a JSON manifest. For diagnostics or for capturing
# a static record when the live server doesn't permit writes
# (public demos, audit modes). Not the gate; smoke-immich is.
refresh-immich-validator: build
	$(BINARY) smoke immich --probe-only --capture api/immich/required-fields.json
	@echo
	@echo "Captured required-field set. Review with:"
	@echo "  git diff api/immich/required-fields.json"

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

smoke:
	go test -tags=smoke ./internal/...

# Pre-tag gate. Runs lint, the unit test suite, and the live
# Immich round-trip smoke against IMMICH_VERSION. Treat this as the contract before
# `git tag`: don't cut a release that doesn't pass all three. The
# smoke is the guard that would have caught the v0.4.3 device-
# field regression.
#
# Requires golangci-lint on PATH (mise: `mise install golangci-lint`).
# Requires IMMICH_BAIRN_HOST/USER/PASSWORD (or IMMICH_BASE_URL/
# IMMICH_API_KEY) so the smoke can reach an Immich.
pre-tag-check: lint test smoke-immich
	@echo
	@echo "✓ lint + unit tests + live Immich smoke passed."
	@echo "  Safe to git tag."

lint:
	golangci-lint run

tidy:
	go mod tidy

clean:
	rm -rf bin/ dist/
