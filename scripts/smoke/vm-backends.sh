#!/usr/bin/env bash
# Run the complete backend/archive acceptance matrix with one matching build.
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd)"
source "${SCRIPT_DIR}/common.sh"
ARCHIVES_ONLY=0
case "${1:-}" in
	"") [[ $# == 0 ]] || smoke_fail "usage: vm-backends.sh [--archives-only]" ;;
	--archives-only) [[ $# == 1 ]] || smoke_fail "usage: vm-backends.sh [--archives-only]"; ARCHIVES_ONLY=1 ;;
	*) smoke_fail "usage: vm-backends.sh [--archives-only]" ;;
esac
YOLOCTL="${YOLOCTL:-${SCRIPT_DIR}/../../bin/yoloctl}"
[[ -x "$YOLOCTL" ]] || smoke_fail "$YOLOCTL not found or not executable"
YOLOCTL="$(cd "$(dirname "$YOLOCTL")" >/dev/null && pwd)/$(basename "$YOLOCTL")"
export YOLOCTL

# Check both adapters before allocating any fixture. Python is a test-driver
# dependency, not a prerequisite of Firecracker image preparation or transport.
smoke_require_backend firecracker
smoke_require_backend lima
command -v python3 >/dev/null 2>&1 || smoke_fail "python3 is required by the smoke test drivers"

# Lima reserves space for ssh.sock's temporary suffix and restored instance
# names. Keep both root and phase directories short, regardless of TMPDIR.
RUN_ROOT="$(mktemp -d /tmp/yb.XXXXXX)"
printf 'phase\tresult\texit_code\n' >"${RUN_ROOT}/summary.tsv"
printf '%s\n' "$YOLOCTL" >"${RUN_ROOT}/binary.txt"
smoke_info "backend acceptance logs and isolated state: $RUN_ROOT"

run_phase() {
	local name="$1" fixture="$2" backend="$3" destination="$4" script="$5" status
	smoke_info "running $name (fixture: ${RUN_ROOT}/${fixture})"
	if (
		# Never inherit a caller's fixture or preservation settings. Each phase
		# gets its own config/cache/workdir; failure retains it for inspection.
		unset SMOKE_OWNS_ROOT SMOKE_CLEANUP_FAILED
		export SMOKE_ROOT="${RUN_ROOT}/${fixture}" SMOKE_SPACE=smoke-live-vm
		export SMOKE_WORKDIR="${SMOKE_ROOT}/workdir"
		export SMOKE_BACKEND="$backend" SMOKE_IMPORT_BACKEND="$destination"
		export SMOKE_KEEP_ON_FAILURE=1 SMOKE_PRESERVE_SPACE=0
		"${SCRIPT_DIR}/${script}"
	) 2>&1 | tee "${RUN_ROOT}/${name}.log"; then
		printf '%s\tPASS\t0\n' "$name" >>"${RUN_ROOT}/summary.tsv"
	else
		status=$?
		printf '%s\tFAIL\t%s\n' "$name" "$status" >>"${RUN_ROOT}/summary.tsv"
		printf 'Stopped at %s; inspect %s (later phases were not run).\n' "$name" "$RUN_ROOT" >&2
		exit "$status"
	fi
}

if [[ "$ARCHIVES_ONLY" == 0 ]]; then
	run_phase firecracker fc firecracker firecracker live-vm.sh
	run_phase lima li lima lima live-vm.sh
fi
run_phase lima-to-firecracker lf lima firecracker space-archive.sh
run_phase firecracker-to-lima fl firecracker lima space-archive.sh
if [[ "$ARCHIVES_ONLY" == 1 ]]; then
	smoke_info "PASS: both cross-backend archive directions"
else
	smoke_info "PASS: both complete live gates and both cross-backend archive directions"
fi
smoke_info "report: ${RUN_ROOT}/summary.tsv"
