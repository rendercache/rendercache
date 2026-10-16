#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
cd "$(dirname "$0")/.."
version=$(cat VERSION)
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]] || { echo 'Invalid VERSION' >&2; exit 1; }
commit=$(git rev-parse HEAD)
output=${1:-dist}
mkdir -p "$output"
# Initial supported artifact: Linux amd64, verified with real Chromium in CI.
# -trimpath/buildvcs=false/buildid= remove local paths and variable VCS/build IDs.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.version=$version -X main.commit=$commit" -o "$output/rendercache" ./cmd/rendercache
python3 scripts/package-release.py "$output" "$version"
rm "$output/rendercache"
