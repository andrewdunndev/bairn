.PHONY: build build-linux build-darwin build-all test smoke lint clean tidy gen gen-famly gen-immich refresh-immich-spec

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

lint:
	golangci-lint run

tidy:
	go mod tidy

clean:
	rm -rf bin/ dist/
