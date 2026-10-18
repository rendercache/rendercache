#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Usage: build-release.sh OUTPUT_DIR [GOOS] [GOARCH]
# Builds one deterministic archive. Archive names carry no version, so
# releases/latest/download links stay valid from one release to the next.
set -euo pipefail
cd "$(dirname "$0")/.."
version=$(tr -d '\r\n' < VERSION)
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]] || { echo 'Invalid VERSION' >&2; exit 1; }
commit=$(git rev-parse HEAD)
output=${1:-dist}
goos=${2:-$(go env GOOS)}
goarch=${3:-$(go env GOARCH)}
case "$goos" in linux|darwin|windows) ;; *) echo "unsupported GOOS $goos" >&2; exit 1 ;; esac
binary=rendercache
[[ "$goos" == windows ]] && binary=rendercache.exe
mkdir -p "$output"
# -trimpath/buildvcs=false/buildid= remove local paths and variable VCS/build IDs.
CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -buildvcs=false \
  -ldflags="-s -w -buildid= -X main.version=$version -X main.commit=$commit" \
  -o "$output/$binary" ./cmd/rendercache
"${PYTHON:-python3}" scripts/package-release.py "$output" "$goos" "$goarch" "$binary"
rm "$output/$binary"
