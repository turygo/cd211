.PHONY: generate lint test build web-preview image image-multiarch

VERSION ?= dev
WEB_PREVIEW_ADDRESS ?= 127.0.0.1:18080

generate:
	./scripts/generate-proto.sh
	GOTOOLCHAIN=go1.26.5 go tool sqlc generate

lint:
	GOTOOLCHAIN=go1.26.5 golangci-lint run

test:
	GOTOOLCHAIN=go1.26.5 go test ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 GOTOOLCHAIN=go1.26.5 go build -ldflags "-X github.com/turygo/cd211/internal/buildinfo.Version=$(VERSION)" -o bin/cd211 ./cmd/cd211

web-preview:
	CD211_WEB_PREVIEW_ADDRESS=$(WEB_PREVIEW_ADDRESS) GOTOOLCHAIN=go1.26.5 go test -ldflags "-X github.com/turygo/cd211/internal/buildinfo.Version=$(VERSION)" ./internal/web -run '^TestWebPreview$$' -count=1 -v

image:
	docker build --build-arg VERSION=$(VERSION) -t cd211:local .

# Mirrors the release build; requires a buildx builder with QEMU for arm64.
image-multiarch:
	docker buildx build --build-arg VERSION=$(VERSION) --platform linux/amd64,linux/arm64 -t cd211:local .
