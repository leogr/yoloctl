#!/usr/bin/env bash
# Local filesystem/SFTP tests only; no yoloctl, VM, space or install operations.
set -eu

if [ "$(uname -s)" != Darwin ]; then
  echo "Run this script on the macOS host." >&2
  exit 2
fi
if [ "$#" -ne 0 ]; then
  echo "Usage: bash scripts/check-vmshare-macos.sh" >&2
  exit 2
fi

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$repo"
out=$(mktemp -d "${TMPDIR:-/tmp}/yoloctl-vmshare-macos.XXXXXXXX")
printf 'Evidence: %s\n' "$out"
{
  git rev-parse HEAD
  git status --short
  uname -a
  sw_vers
  go version
  go env GOOS GOARCH GOHOSTOS GOHOSTARCH CGO_ENABLED GOTOOLCHAIN
  printf 'inherited umask: '
  umask
} > "$out/environment.txt" 2>&1
cat "$out/environment.txt"

result=0
for mask in 022 077; do
  printf 'umask %s: vmshare tests (three repetitions)\n' "$mask"
  if (umask "$mask"; go test -v -count=3 ./internal/vmshare) > "$out/unit-$mask.log" 2>&1; then
    printf 'PASS unit-%s\n' "$mask"
  else
    result=1
    printf 'FAIL unit-%s (see log)\n' "$mask"
  fi
  printf 'umask %s: vmshare race tests\n' "$mask"
  if (umask "$mask"; go test -race -v -count=1 ./internal/vmshare) > "$out/race-$mask.log" 2>&1; then
    printf 'PASS race-%s\n' "$mask"
  else
    result=1
    printf 'FAIL race-%s (see log; race tests require a working C toolchain)\n' "$mask"
  fi
done
printf '%s\n' "$result" > "$out/exit-status.txt"
printf 'Final exit=%s; retained evidence: %s\n' "$result" "$out"
exit "$result"
