#!/bin/sh
# Regenerate the SDK-compatible gogo binding from the shared V2 schema and
# require byte-for-byte parity with the checked-in file. protoc 3.19.6 and
# github.com/cosmos/gogoproto v1.7.0 produced the committed binding.
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/../../../.." && pwd)
tmp_dir=$(mktemp -d)
trap 'rm -r -- "$tmp_dir"' EXIT

mkdir -p "$tmp_dir/src/alpha/authzattrs/v2" "$tmp_dir/out/alpha/authzattrs/v2"
cp "$repo_dir/internal/authorization/pb/v2/certificate.proto" "$tmp_dir/src/alpha/authzattrs/v2/certificate.proto"
cd "$repo_dir"
GOTOOLCHAIN=go1.24.13 GOCACHE="${GOCACHE:-$tmp_dir/go-cache}" \
  go build -o "$tmp_dir/protoc-gen-gogofaster" github.com/cosmos/gogoproto/protoc-gen-gogofaster
protoc --plugin="protoc-gen-gogofaster=$tmp_dir/protoc-gen-gogofaster" \
  --gogofaster_out="paths=source_relative:$tmp_dir/out" \
  -I "$tmp_dir/src" alpha/authzattrs/v2/certificate.proto

if ! cmp -s "$repo_dir/internal/authorization/pb/sdkv2/certificate.pb.go" \
    "$tmp_dir/out/alpha/authzattrs/v2/certificate.pb.go"; then
  echo 'SDK V2 protobuf binding differs from the generated schema' >&2
  exit 1
fi
echo 'PASS SDK V2 protobuf generation parity'
