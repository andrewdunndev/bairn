.PHONY: build build-linux build-darwin build-all test smoke lint clean tidy gen gen-famly gen-immich

BINARY := bin/bairn

# Codegen: regenerate typed clients from specs.
# api/famly  ← genqlient against api/famly/schema.graphql
# api/immich ← oapi-codegen against api/immich/openapi.json
gen: gen-famly gen-immich

gen-famly:
	cd api/famly && go tool genqlient

gen-immich:
	cd api/immich && go tool oapi-codegen --config oapi-codegen.yaml openapi.json

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
