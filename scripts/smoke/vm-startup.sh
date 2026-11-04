#!/usr/bin/env bash
# Standalone VM creation/restart measurements in a fresh disposable space.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
[[ "${SMOKE_BACKEND:-lima}" == lima ]] || smoke_fail "vm-startup measures Lima provisioning; use SMOKE_BACKEND=lima"
[[ -z "${SMOKE_ROOT:-}" ]] || smoke_fail "vm-startup requires a fresh root; unset SMOKE_ROOT"
[[ -z "${SMOKE_WORKDIR:-}" ]] || smoke_fail "vm-startup requires a disposable workdir; unset SMOKE_WORKDIR"
command -v python3 >/dev/null 2>&1 || smoke_fail "python3 is required for monotonic timing"
VM_START_SAMPLES="${VM_START_SAMPLES:-3}"
VM_START_SETTLE_SECONDS="${VM_START_SETTLE_SECONDS:-10}"
[[ "$VM_START_SAMPLES" =~ ^([1-9]|1[0-9]|20)$ ]] || smoke_fail "VM_START_SAMPLES must be 1..20"
[[ "$VM_START_SETTLE_SECONDS" =~ ^([0-9]|[1-5][0-9]|60)$ ]] || smoke_fail "VM_START_SETTLE_SECONDS must be 0..60"
SMOKE_SPACE="${SMOKE_SPACE:-smoke-startup}"
smoke_init

cleanup() {
	local status=$?
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" == 1 || ( "$status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ) ]]; then
		smoke_info "isolated fixture retained: $SMOKE_ROOT"
	else
		if [[ -f "${SMOKE_CONFIG_ROOT}/spaces/${SMOKE_SPACE}/space.yaml" ]]; then
			smoke_destroy_space || status=1
		fi
		smoke_remove_owned_root
	fi
	if [[ -n "${report:-}" ]]; then
		smoke_info "measurement reports retained: $report"
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Retain measurements independently from disposable VM/config/cache cleanup.
report="$(mktemp -d "${TMPDIR:-/tmp}/yoloctl-startup.XXXXXX")"
smoke_info "fixture: $SMOKE_ROOT; reports: $report"
{
	date -u
	uname -a
	limactl --version
	printf 'binary=%s\nrestart_samples=%s\nsettle_seconds=%s\n' "$YOLOCTL" "$VM_START_SAMPLES" "$VM_START_SETTLE_SECONDS"
} > "$report/host.txt"

measure() {
	local phase="$1"
	shift
	smoke_info "measuring $phase"
	python3 - "$report/timings.jsonl" "$phase" "$@" <<'PY' 2>&1 | tee "$report/$phase.log"
import json
import subprocess
import sys
import time

path, phase, *command = sys.argv[1:]
started = time.monotonic()
result = subprocess.run(command, check=False)
seconds = time.monotonic() - started
record = {"phase": phase, "seconds": seconds, "exit_code": result.returncode}
with open(path, "a", encoding="utf-8") as output:
    output.write(json.dumps(record) + "\n")
print(f"{phase}: {seconds:.3f}s (exit {result.returncode})", flush=True)
sys.exit(result.returncode if result.returncode >= 0 else 128 - result.returncode)
PY
}

snapshot() {
	local phase="$1"
	# Same settling interval in both variants, outside the measured CLI time.
	sleep "$VM_START_SETTLE_SECONDS"
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- bash -c '
set -e
cat /etc/os-release
uname -r
cat /proc/meminfo
systemctl is-active containerd.service yoloctl-controller.service
printf "%s\n" "OpenShell is installed per box from qualified controller artifacts (no VM-side release download)."
dpkg-query -W ca-certificates curl socat python3
systemctl list-units --type=service --state=running --no-pager
systemd-analyze time || true
' > "$report/$phase-guest.txt"
	# Ordinary declaration reads prove that the selected controller is usable.
	"$YOLOCTL" account list --space "$SMOKE_SPACE" > "$report/$phase-accounts.txt"
}

measure create "$YOLOCTL" space create "$SMOKE_SPACE" --workdir "$SMOKE_WORKDIR" --account default \
	"${SMOKE_VM_ARGS[@]}" --memory "${SMOKE_VM_MEMORY:-4GiB}" --cpus "${SMOKE_VM_CPUS:-4}"
snapshot create
for ((sample = 1; sample <= VM_START_SAMPLES; sample++)); do
	measure "stop-$sample" "$YOLOCTL" space stop "$SMOKE_SPACE"
	measure "start-$sample" "$YOLOCTL" space start "$SMOKE_SPACE"
	snapshot "start-$sample"
done
smoke_info "PASS: VM creation, restart and controller-read checks; inspect reports for timings and memory"
