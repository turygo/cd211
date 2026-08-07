.PHONY: generate test build image

generate:
	./scripts/generate-proto.sh
	GOTOOLCHAIN=go1.26.5 go tool sqlc generate

test:
	GOTOOLCHAIN=go1.26.5 go test ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 GOTOOLCHAIN=go1.26.5 go build -o bin/cd211 ./cmd/cd211

image:
	docker build -t cd211:local .
