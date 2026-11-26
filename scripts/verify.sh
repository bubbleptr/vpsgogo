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
  step go test ./...                           # includes the views, through ui.Tester
  step scripts/build-agent.sh
  cd "$root/apps/desktop"
  step go tool mygo doctor
  step go tool mygo build -platform darwin/arm64 -skip-dmg -skip-notarize
  cd "$root"
}

m1() {
  step go vet -tags integration ./...
  step go test -race -count=1 ./packages/...
  step go test -tags integration -count=1 -v ./test/e2e/...   # Docker sshd: Debian 12, Ubuntu 24.04
}

m2() {
  step go vet -tags vm ./...
  step go test -race -count=1 ./packages/core/provisioning/...
  # Recreates the lima VM test/e2e/lima/fresh-vps.yaml (Debian 12); local only.
  step go test -tags vm -count=1 -v -timeout 40m ./test/e2e/...
}

case "${1:-}" in
  M0) m0 ;;
  M1) m1 ;;
  M2) m2 ;;
  *) echo "usage: $0 M0|M1|M2   (later milestones are added as they are built)" >&2; exit 2 ;;
esac

echo
echo "verify $1: OK"
