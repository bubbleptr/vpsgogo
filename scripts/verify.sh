#!/usr/bin/env bash
# Runs the automatic checks of one milestone (docs/SPEC.md §8), printing each
# command and its output. Usage: scripts/verify.sh M<n>
set -euo pipefail
cd "$(dirname "$0")/.."
root=$PWD

step() {
  echo
  echo "==> $*"
  "$@"
}

m0() {
  step go build ./...
  step go vet ./...
  step go test ./...
  step scripts/build-agent.sh
  cd "$root/apps/desktop"
  step bun install --frozen-lockfile
  step bun run generate
  step git diff --exit-code -- src/mygo.ts   # the committed client matches the Go code
  step bun run test
  step bun run build:web
  step bun run build:web:mock                  # pages build against the mock Go side
  step bun x mygo doctor
  cd "$root"
}

m1() {
  step go vet -tags integration ./...
  step go test -race -count=1 ./packages/...
  step go test -tags integration -count=1 -v ./test/e2e/...   # Docker sshd: Debian 12, Ubuntu 24.04
}

case "${1:-}" in
  M0) m0 ;;
  M1) m1 ;;
  *) echo "usage: $0 M0|M1   (later milestones are added as they are built)" >&2; exit 2 ;;
esac

echo
echo "verify $1: OK"
