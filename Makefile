.PHONY: build build-linux build-darwin build-all test smoke lint clean tidy gen gen-famly gen-immich refresh-immich-spec refresh-immich-validator smoke-immich pre-tag-check

BINARY := bin/bairn

# Codegen: regenerate typed clients from specs.
# api/famly  ← genqlient against api/famly/schema.graphql
# api/immich ← oapi-codegen against api/immich/openapi.json
gen: gen-famly gen-immich

gen-famly:
	cd api/famly && go tool genqlient

gen-immich:
	cd api/immich && go tool oapi-codegen --config oapi-codegen.yaml openapi.json

# Re-vendor api/immich/openapi.json from immich-app/immich main and
# regenerate the typed client. Run before tagging if upstream drift
# is suspected, or whenever Renovate flags the vendored file. The
# committed spec is the contract bairn validates against; refresh
# is the maintenance loop.
#
# Famly's schema is captured locally via introspection; their
# clients are not publicly published. No analogous refresh target.
refresh-immich-spec:
	curl -sSfLo api/immich/openapi.json \
	  https://raw.githubusercontent.com/immich-app/immich/main/open-api/immich-openapi-specs.json
	$(MAKE) gen-immich
	@echo "Refreshed Immich spec + regenerated client."
	@echo "Review 'git diff api/immich/' before committing."

# Run the full Immich smoke locally: login + mint ephemeral API
# key + upload tiny JPEG + assert + delete asset + delete API key.
# Useful before tagging if you touched anything in api/immich/ or
# internal/sink/immich.
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

build:
	mkdir -p bin
	go build -o $(BINARY) ./cmd/bairn

build-linux:
	mkdir -p bin
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w" \
		-o $(BINARY)-linux-amd64 ./cmd/bairn

build-darwin:
	mkdir -p bin
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w" \
		-o $(BINARY)-darwin-arm64 ./cmd/bairn

build-all: build-linux build-darwin

test:
	go test -race ./...

smoke:
	go test -tags=smoke ./internal/...

# Pre-tag gate. Runs lint, the unit test suite, and the live
# Immich round-trip smoke. Treat this as the contract before
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
