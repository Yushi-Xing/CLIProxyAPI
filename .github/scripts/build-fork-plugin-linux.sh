#!/usr/bin/env bash
set -euo pipefail

# Run in the same manylinux2014 image as the upstream Linux plugin build.
: "${TAG:?release tag is required}"
: "${REVISION:?release revision is required}"
: "${BUILD_DATE:?build date is required}"
export PATH="/usr/local/go/bin:$PATH"
export GOPATH=/go

archive_dir=dist/plugin-archive
mkdir -p "$archive_dir"
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags="-s -w -X main.Version=${TAG} -X main.Commit=${REVISION} -X main.BuildDate=${BUILD_DATE}" \
  -o "$archive_dir/cli-proxy-api" ./cmd/server

go version -m "$archive_dir/cli-proxy-api" | grep -q 'CGO_ENABLED=1'
readelf -l "$archive_dir/cli-proxy-api" | grep -q 'Requesting program interpreter'
glibc_versions="$(readelf --version-info "$archive_dir/cli-proxy-api" | sed -n 's/.*Name: GLIBC_\([0-9.]*\).*/\1/p' | sort -Vu)"
if [[ -z "$glibc_versions" ]]; then
  echo 'plugin build must report its GLIBC requirements' >&2
  exit 1
fi
max_glibc="$(printf '%s\n' "$glibc_versions" | tail -n 1)"
if [[ "$(printf '2.17\n%s\n' "$max_glibc" | sort -V | tail -n 1)" != '2.17' ]]; then
  printf 'plugin build requires GLIBC_%s, expected 2.17 or older\n' "$max_glibc" >&2
  exit 1
fi
"$archive_dir/cli-proxy-api" --help > /tmp/cli-proxy-api-plugin-help.txt 2>&1
head -n 1 /tmp/cli-proxy-api-plugin-help.txt
cp LICENSE README.md README_CN.md config.example.yaml "$archive_dir/"
tar -C "$archive_dir" -czf "dist/CLIProxyAPI_${TAG}_linux_amd64.tar.gz" \
  cli-proxy-api LICENSE README.md README_CN.md config.example.yaml
