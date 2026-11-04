#!/usr/bin/env bash
# Automatic cleanup in a disposable space; never fills a user's system disk.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
cleanup() {
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT
smoke_ensure_space
vm() { "$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- "$@"; }

if [[ "$SMOKE_BACKEND" == firecracker ]]; then
    smoke_info "storage GC: host backing-storage guard is active"
    "$YOLOCTL" space show "$SMOKE_SPACE" --json > "${SMOKE_ROOT}/gc-space.json"
    python3 - "${SMOKE_ROOT}/gc-space.json" <<'PY_HOST_STORAGE'
import json, pathlib, socket, sys
space = json.loads(pathlib.Path(sys.argv[1]).read_text())
assert space['status'] == 'Running', space.get('status_detail', space['status'])
state = json.loads((pathlib.Path(space['dir']) / 'runtime.json').read_text())
with socket.socket(socket.AF_UNIX) as s:
    s.settimeout(2)
    s.connect(str(pathlib.Path(state['dir']) / 'storage.sock'))
    with s.makefile('rb') as f:
        status = json.loads(f.read(16385))
assert status == {'paused': False}, status
PY_HOST_STORAGE
fi

smoke_info "storage GC: native BuildKit budget is installed"
vm python3 - <<'PY'
import os, tomllib
with open('/etc/buildkit/buildkitd.toml', 'rb') as f:
    cfg = tomllib.load(f)
v = os.statvfs('/')
size = v.f_blocks * v.f_frsize
w = cfg['worker']['containerd']
assert w['gc'] and w['enabled'] and w['namespace'] == 'default'
p, = w['gcpolicy']
assert p['all'] and p['maxUsedSpace'] == min(4*1024**3, size//10)
assert p['minFreeSpace'] == size//5
assert p['reservedSpace'] == min(256*1024**2, size//10)
PY
if [[ "$SMOKE_BACKEND" == firecracker ]]; then vm /sbin/yoloctl-init buildkit; fi
vm buildctl debug workers -v > "${SMOKE_ROOT}/gc-workers.log"

smoke_info "storage GC: declarations, stopped boxes and workdir data are protected"
"$YOLOCTL" box create gc-stopped --space "$SMOKE_SPACE" --agent none --workdir-mode ephemeral --no-attach --no-autoupdate
"$YOLOCTL" box exec gc-stopped --space "$SMOKE_SPACE" -- sh -c 'printf keep-me > /workdir/retained'
"$YOLOCTL" box stop gc-stopped --space "$SMOKE_SPACE"
# The declared custom alias must protect its matching composed tag.
"$YOLOCTL" box create gc-declared --space "$SMOKE_SPACE" --agent none --workdir-mode ephemeral \
	--image local/gc-desired:latest --manifest-only --no-attach

# Pause only this fixture's controller while root prepares image metadata.
if [[ "$SMOKE_BACKEND" == firecracker ]]; then
    vm /sbin/yoloctl-init controller stop
else
    vm systemctl stop yoloctl-controller.service
fi
vm bash -se <<'SH'
root=$(mktemp -d /tmp/yoloctl-gc-smoke.XXXXXX)
trap 'rm -rf "$root"' EXIT
cd "$root"
for kind in desired runtime obsolete foreign; do
    printf 'FROM yoloctl/agentbox:ubuntu-24.04\nRUN echo %s > /gc-smoke-kind\nLABEL smoke.gc.kind="%s"\n' "$kind" "$kind" > Dockerfile
    if [ "$kind" != foreign ]; then
        printf 'LABEL yoloctl.kind="composed"\n' >> Dockerfile
    fi
    nerdctl build -t "yoloctl/agentbox-composed:gc-$kind" .
done
nerdctl tag yoloctl/agentbox-composed:gc-desired local/gc-desired:latest
nerdctl tag yoloctl/agentbox-composed:gc-runtime local/gc-moved:latest
# Metadata only: no workload bypasses OpenShell. After retagging the creation
# reference, only the immutable container identity protects gc-runtime.
nerdctl create --name gc-external --entrypoint /bin/false local/gc-moved:latest
nerdctl tag yoloctl/agentbox-composed:gc-foreign local/gc-moved:latest
SH

smoke_info "storage GC: controller startup automatically removes only the obsolete tag"
if [[ "$SMOKE_BACKEND" == firecracker ]]; then
    gc_vm_log="$("$YOLOCTL" space show "$SMOKE_SPACE" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["host_agent_log_path"])')"
    gc_log_offset="$(python3 -c 'import os,sys; print(os.path.getsize(sys.argv[1]))' "$gc_vm_log")"
fi
vm python3 - <<'PY'
import json, os, re, subprocess, time

def inspect(ref):
    p = subprocess.run(['nerdctl','image','inspect',ref],capture_output=True)
    return json.loads(p.stdout)[0] if p.returncode == 0 else None

def external_container():
    # Use standard JSON output entirely inside the VM: no Go template or
    # JSON argument round-trip through the host and SSH shell.
    p = subprocess.check_output(['nerdctl','inspect','gc-external'])
    c = json.loads(p)[0]
    return {'Id': c['Id'], 'State': c['State']}

# nerdctl 2.3.5 leaves State.Status empty when no task was ever started.
# Require the same unstarted container/state, not Docker's "created" spelling.
before = external_container()
assert before['State']['Running'] is False and before['State']['Pid'] == 0, f"external fixture is running: {before['State']}"
microvm = os.path.exists('/run/yoloctl-vm/control.sock')
subprocess.run(['/sbin/yoloctl-init','controller','start'] if microvm else ['systemctl','start','yoloctl-controller.service'], check=True)
if not microvm:
    invocation = subprocess.check_output(['systemctl','show','-p','InvocationID','--value','yoloctl-controller.service'], text=True).strip()
    assert re.fullmatch('[0-9a-f]{32}', invocation), 'missing controller journal invocation'
end = time.monotonic() + 60
while True:
    journal = ''
    if not microvm:
        journal = subprocess.check_output(['journalctl','_SYSTEMD_INVOCATION_ID='+invocation,'--no-pager','-o','cat'], text=True)
    # Tag removal precedes cache pruning. This summary is written only after
    # buildctl succeeds and disk usage is remeasured; isolate this startup.
    if inspect('yoloctl/agentbox-composed:gc-obsolete') is None and (microvm or re.search(r'storage cleanup: [1-9][0-9]* obsolete image\(s\) removed', journal)):
        break
    if time.monotonic() >= end:
        print(journal[-16000:])
        raise SystemExit('automatic image/cache cleanup did not complete')
    time.sleep(1)
for kind in ['desired','runtime','foreign']:
    assert inspect('yoloctl/agentbox-composed:gc-'+kind), kind
after = external_container()
assert after['Id'] == before['Id'], 'GC replaced the external container'
assert after['State'] == before['State'], f"GC changed container state: {before['State']} -> {after['State']}"
PY
if [[ "$SMOKE_BACKEND" == firecracker ]]; then
    # PID 1's controller output goes to the host VM serial log, not journald.
    # Use the pre-start byte offset so an earlier sweep cannot satisfy this check.
    python3 - "$gc_vm_log" "$gc_log_offset" <<'PY_GC_LOG'
import re, sys, time
end = time.monotonic() + 60
while True:
    with open(sys.argv[1], 'rb') as log:
        log.seek(int(sys.argv[2]))
        data = log.read()
    if re.search(rb'storage cleanup: [1-9][0-9]* obsolete image\(s\) removed', data):
        break
    if time.monotonic() >= end:
        raise SystemExit('automatic image/cache cleanup did not complete: ' + repr(data[-16000:]))
    time.sleep(1)
PY_GC_LOG
fi

"$YOLOCTL" image prune --space "$SMOKE_SPACE" --dry-run > "${SMOKE_ROOT}/gc-dry-run.log"
grep -q 'would prune 0 composed image(s)' "${SMOKE_ROOT}/gc-dry-run.log" || smoke_fail "protected images appeared in prune plan"
"$YOLOCTL" box start gc-stopped --space "$SMOKE_SPACE" --no-attach --no-autoupdate
[[ "$("$YOLOCTL" box exec gc-stopped --space "$SMOKE_SPACE" -- cat /workdir/retained)" == keep-me ]] || smoke_fail "workdir data changed"

smoke_info "storage GC: last declaration removal schedules idle cleanup"
"$YOLOCTL" box destroy gc-declared --space "$SMOKE_SPACE" --force
vm python3 - <<'PY'
import subprocess, time
end=time.monotonic()+60
while subprocess.run(['nerdctl','image','inspect','yoloctl/agentbox-composed:gc-desired'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode == 0:
    if time.monotonic() >= end: raise SystemExit('post-mutation cleanup did not run')
    time.sleep(1)
# Removing the disposable composed alias must preserve this user image name.
subprocess.run(['nerdctl','image','inspect','local/gc-desired:latest'],stdout=subprocess.DEVNULL,check=True)
PY
smoke_destroy_box gc-stopped
vm nerdctl rm gc-external > /dev/null
smoke_info "PASS: automatic image GC, BuildKit policy, immutable identities and retained data"
