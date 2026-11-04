#!/usr/bin/env bash
# Invoked by scripts/vm/firecracker/linux7.py after candidate verification.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd)"
source "${SCRIPT_DIR}/common.sh"
[[ -n "${SMOKE_EXPECT_KERNEL_RELEASE:-}" && -n "${SMOKE_VM_IMAGE:-}" ]] ||
    smoke_fail 'use scripts/vm/firecracker/linux7.py smoke to verify the candidate first'
[[ "$SMOKE_VM_IMAGE" == "${SMOKE_UPDATE_VM_IMAGE:-}" ]] ||
    smoke_fail 'the image-update phase must retain the candidate kernel'
smoke_init

cleanup() {
    local status=$?
    trap - EXIT
    if [[ "$status" != 0 ]]; then
        printf 'Linux 7 gate failed; private fixture and logs retained at %s\n' "$SMOKE_ROOT" >&2
    fi
    exit "$status"
}
trap cleanup EXIT

smoke_ensure_space
smoke_info "Linux 7: checking guest kernel, confinement support and memory telemetry"
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- python3 - "$SMOKE_EXPECT_KERNEL_RELEASE" <<'PY' | tee "${SMOKE_ROOT}/kernel-check.json"
import ctypes, gzip, json, os, pathlib, platform, sys
release = platform.release()
assert release == sys.argv[1], (release, sys.argv[1])
assert platform.machine() == 'x86_64'
config = gzip.decompress(pathlib.Path('/proc/config.gz').read_bytes()).decode().splitlines()
required = ['SECURITY_LANDLOCK', 'SECCOMP_FILTER', 'MEMCG', 'CGROUP_PIDS',
            'CFS_BANDWIDTH', 'OVERLAY_FS', 'FUSE_FS', 'NF_TABLES', 'PSI']
assert all('CONFIG_' + name + '=y' in config for name in required)
controllers = pathlib.Path('/sys/fs/cgroup/cgroup.controllers').read_text().split()
assert {'cpu', 'memory', 'pids'} <= set(controllers), controllers
pressure = pathlib.Path('/proc/pressure/memory').read_text()
assert 'some avg10=' in pressure and 'full avg10=' in pressure
libc = ctypes.CDLL(None, use_errno=True)
abi = libc.syscall(ctypes.c_long(444), ctypes.c_void_p(), ctypes.c_size_t(0), ctypes.c_uint(1))
assert abi > 0, ('Landlock ABI query failed', abi, ctypes.get_errno())
print(json.dumps({'kernel': release, 'landlock_abi': abi, 'controllers': controllers,
                  'memory_pressure': pressure, 'cpus': os.cpu_count()}, indent=2))
PY

# Existing gate exercises builds, confined egress, PTYs, policy changes, shares,
# restarts, storage cleanup, image replacement and native-account persistence.
bash "${SCRIPT_DIR}/live-vm.sh"
smoke_info "PASS: Linux ${SMOKE_EXPECT_KERNEL_RELEASE} isolated compatibility gate"
