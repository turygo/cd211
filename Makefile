.PHONY: generate lint test build image image-multiarch

generate:
	./scripts/generate-proto.sh
	GOTOOLCHAIN=go1.26.5 go tool sqlc generate

lint:
	GOTOOLCHAIN=go1.26.5 golangci-lint run

test:
	GOTOOLCHAIN=go1.26.5 go test ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 GOTOOLCHAIN=go1.26.5 go build -o bin/cd211 ./cmd/cd211

image:
	docker build -t cd211:local .

# Mirrors the release build; requires a buildx builder with QEMU for arm64.
image-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 -t cd211:local .
