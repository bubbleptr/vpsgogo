#!/usr/bin/env bash
# Cross-compiles gogo-agent for the VPS targets into apps/desktop/embed/agent/,
# where the desktop app embeds it (docs/SPEC.md §2.2).
set -euo pipefail
cd "$(dirname "$0")/.."

version="${VERSION:-0.1.0-dev}"
out=apps/desktop/embed/agent
mkdir -p "$out"
for arch in amd64 arm64; do
  bin="$out/gogo-agent-linux-$arch"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$bin" ./apps/agent/cmd/gogo-agent
  (cd "$out" && shasum -a 256 "$(basename "$bin")" > "$(basename "$bin").sha256")
  echo "built $bin"
done
