#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
protoc=$("$repo_root/scripts/install-protoc.sh")
protoc_dir=${protoc%/bin/protoc}
proto_dir="$repo_root/third_party/clouddrive2"
output_dir="$repo_root/internal/clouddrive/pb"
go_bin="$repo_root/.tools/go/bin"

go_install() {
	binary=$1
	module=$2
	if [ ! -x "$go_bin/$binary" ]; then
		GOBIN="$go_bin" GOTOOLCHAIN=go1.26.5 go install "$module"
	fi
}

mkdir -p "$go_bin" "$output_dir"
go_install protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
go_install protoc-gen-go-grpc google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2

PATH="$go_bin:$PATH" "$protoc" \
	--proto_path="$proto_dir" \
	--proto_path="$protoc_dir/include" \
	--go_out=paths=source_relative:"$output_dir" \
	--go_opt=Mclouddrive.proto=github.com/turygo/cd211/internal/clouddrive/pb \
	--go-grpc_out=paths=source_relative:"$output_dir" \
	--go-grpc_opt=Mclouddrive.proto=github.com/turygo/cd211/internal/clouddrive/pb \
	clouddrive.proto
