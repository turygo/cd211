#!/bin/sh
set -eu

version=35.1
repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
os=$(uname -s)
arch=$(uname -m)

case "$os/$arch" in
	Darwin/arm64)
		host=darwin-arm64
		asset=osx-aarch_64
		checksum=193289af0470c6a1aada357d4fba0bbf8d78bfaac8b5e42ca30af2ef75583de2
		;;
	Linux/x86_64)
		host=linux-amd64
		asset=linux-x86_64
		checksum=6930ebf62bd4ea607b98fff052596c6ee564b9835b4ce172c75a3f53ae9d91b7
		;;
	Linux/aarch64|Linux/arm64)
		host=linux-arm64
		asset=linux-aarch_64
		checksum=01bf9d08808c7f96678b63f4bd8efa559bb4f83d5a7a270d5edaf507f9d5d9cf
		;;
	*)
		printf 'unsupported host: %s/%s (supported: darwin/arm64, linux/amd64, linux/arm64)\n' "$os" "$arch" >&2
		exit 1
		;;
esac

install_dir="$repo_root/.tools/protoc-$version/$host"
protoc="$install_dir/bin/protoc"
if [ -x "$protoc" ]; then
	printf '%s\n' "$protoc"
	exit 0
fi

for command in curl unzip; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf 'required command not found: %s\n' "$command" >&2
		exit 1
	fi
done


archive="protoc-$version-$asset.zip"
archive_path="$install_dir/$archive"
archive_url="https://github.com/protocolbuffers/protobuf/releases/download/v$version/$archive"
mkdir -p "$install_dir"

if [ ! -f "$archive_path" ]; then
	temporary_archive="$archive_path.tmp"
	trap 'rm -f "$temporary_archive"' EXIT HUP INT TERM
	curl --fail --location --silent --show-error --output "$temporary_archive" "$archive_url"
	mv "$temporary_archive" "$archive_path"
	trap - EXIT HUP INT TERM
fi
if command -v shasum >/dev/null 2>&1; then
	actual_checksum=$(shasum -a 256 "$archive_path")
elif command -v sha256sum >/dev/null 2>&1; then
	actual_checksum=$(sha256sum "$archive_path")
else
	printf 'required command not found: shasum or sha256sum\n' >&2
	exit 1
fi


actual_checksum=${actual_checksum%% *}
if [ "$actual_checksum" != "$checksum" ]; then
	printf 'checksum verification failed for %s\n' "$archive" >&2
	rm -f "$archive_path"
	exit 1
fi

staging_dir="$install_dir/.extract.$$"
trap 'rm -rf "$staging_dir"' EXIT HUP INT TERM
mkdir "$staging_dir"
unzip -q "$archive_path" -d "$staging_dir"
if [ ! -x "$staging_dir/bin/protoc" ]; then
	printf 'protoc archive did not contain bin/protoc\n' >&2
	exit 1
fi

rm -rf "$install_dir/bin" "$install_dir/include"
mv "$staging_dir/bin" "$install_dir/bin"
mv "$staging_dir/include" "$install_dir/include"
trap - EXIT HUP INT TERM
rm -rf "$staging_dir"
printf '%s\n' "$protoc"
