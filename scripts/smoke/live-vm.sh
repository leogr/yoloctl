#!/usr/bin/env bash
# Combined isolated live-VM smoke gate.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd)"
source "${SCRIPT_DIR}/common.sh"
smoke_init
SMOKE_PRESERVE_SPACE=1
export SMOKE_PRESERVE_SPACE

cleanup() {
	local status=$?
	set +e
	if [[ "$status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ]]; then
		printf 'Smoke failed; isolated VM and logs retained at %s\n' "$SMOKE_ROOT" >&2
		exit "$status"
	fi
	smoke_destroy_space || status=1
	smoke_remove_owned_root
	if [[ "$status" == 0 ]]; then
		smoke_info "PASS: complete isolated live-VM smoke gate"
	fi
	exit "$status"
}
trap cleanup EXIT

smoke_info "live-VM smoke root: $SMOKE_ROOT"
smoke_info "all state is isolated from the caller's yoloctl configuration"

"${SCRIPT_DIR}/space-lifecycle.sh"
"${SCRIPT_DIR}/image-ensure.sh"
"${SCRIPT_DIR}/box-stop.sh"
"${SCRIPT_DIR}/memory.sh"
"${SCRIPT_DIR}/features-as-policy.sh"
"${SCRIPT_DIR}/npm.sh"
"${SCRIPT_DIR}/pip.sh"
"${SCRIPT_DIR}/storage-gc.sh"
"${SCRIPT_DIR}/agent-state.sh"
"${SCRIPT_DIR}/box-attach.sh"
"${SCRIPT_DIR}/host-integration.sh"
"${SCRIPT_DIR}/controller-operator.sh"
"${SCRIPT_DIR}/space-archive.sh"
"${SCRIPT_DIR}/gemini.sh"
"${SCRIPT_DIR}/agy.sh"
"${SCRIPT_DIR}/pi.sh"
"${SCRIPT_DIR}/opencode.sh"
