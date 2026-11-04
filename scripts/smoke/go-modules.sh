#!/usr/bin/env bash
# Isolated Go development environment and confined module-proxy smoke.
#
# The go feature's network.go-modules grant names the resolved binary
# /usr/local/go/bin/go (OpenShell attributes egress to it, not to the
# /usr/local/bin/go symlink on PATH). From an empty module cache the box must
# download, verify and build a module dependency; curl must not borrow the
# grant; and GOTOOLCHAIN=local must refuse a go.mod that needs a newer Go with
# Go's own message instead of downloading a toolchain no policy can grant.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

BOX=smoke-go
cleanup() {
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
		if [[ "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 && "${SMOKE_PASSED:-0}" != 1 ]]; then
			printf 'Retaining failed fixture at %s\n' "$SMOKE_ROOT" >&2
			return
		fi
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT

smoke_ensure_space
smoke_destroy_box "$BOX"
smoke_info "go: Go environment with confined module access"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent none \
	--features go --workdir-mode ephemeral --no-attach --no-autoupdate
in_box() { "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- "$@"; }

smoke_info "go: toolchain, empty module cache, download, verify and build"
in_box sh -se <<'SH'
# Each check says what it saw before failing.
want() { [ "$2" = "$3" ] || { echo "$1 is '$2', want '$3'" >&2; exit 1; }; }
[ "$(id -u)" != 0 ] || { echo "running as root" >&2; exit 1; }
go version
go env GOTOOLCHAIN GOSUMDB GOROOT
want "resolved go" "$(readlink -f "$(command -v go)")" /usr/local/go/bin/go
want "go env GOTOOLCHAIN" "$(go env GOTOOLCHAIN)" local
want "go env GOSUMDB" "$(go env GOSUMDB)" sum.golang.org

# A fresh cache: nothing may be satisfied locally.
GOMODCACHE="$(mktemp -d /tmp/yoloctl-gomod.XXXXXX)"
export GOMODCACHE GOFLAGS=-modcacherw
mkdir -p /workdir/gomod && cd /workdir/gomod
cat >go.mod <<'MOD'
module example.com/yoloctlsmoke

go 1.21
MOD
cat >main.go <<'GO'
package main

import (
	"fmt"

	"golang.org/x/term"
)

func main() { fmt.Println("terminal:", term.IsTerminal(0)) }
GO
go get golang.org/x/term@latest
grep -q '^golang.org/x/term ' go.sum
go mod verify
go build -o smoke .
./smoke </dev/null | grep -q '^terminal: false$'
rm -rf "$GOMODCACHE"

# GOTOOLCHAIN=local: a newer go.mod fails locally, never by downloading.
mkdir -p /workdir/gonewer && cd /workdir/gonewer
printf 'module example.com/newer\n\ngo 1.999\n' >go.mod
printf 'package main\n\nfunc main() {}\n' >main.go
if go build ./... 2>build.err; then
	echo "a go.mod requiring go 1.999 built" >&2
	exit 1
fi
grep -q 'GOTOOLCHAIN=local' build.err || { cat build.err >&2; exit 1; }
SH

smoke_info "go: curl cannot borrow the module-proxy grant"
if in_box curl -fsS --max-time 5 -o /dev/null https://proxy.golang.org/golang.org/x/term/@v/list 2>/dev/null; then
	smoke_fail "curl borrowed Go module proxy egress"
fi
# The denial reaches the sandbox log asynchronously.
logs=""
for _attempt in {1..10}; do
	logs="$("$YOLOCTL" box logs "$BOX" --space "$SMOKE_SPACE" 2>&1 || true)"
	if grep -q "binary '/usr/bin/curl' not allowed in policy 'go-modules'" <<<"$logs"; then
		break
	fi
	sleep 1
done
if ! grep -q "binary '/usr/bin/curl' not allowed in policy 'go-modules'" <<<"$logs"; then
	printf '%s\n' "$logs" | tail -20 >&2
	smoke_fail "missing attributed curl denial"
fi
if grep -q "binary '/usr/local/go/bin/go' not allowed" <<<"$logs"; then
	smoke_fail "the go binary was denied module egress"
fi

diff_output="$("$YOLOCTL" box diff "$BOX" --space "$SMOKE_SPACE" 2>&1)"
[[ "$diff_output" == "PLAN: no changes" ]] || {
	printf '%s\n' "$diff_output" >&2
	smoke_fail "go environment has unexpected reconcile drift"
}
SMOKE_PASSED=1
smoke_info "PASS: go module download/verify/build, local toolchain and confined proxy access"
