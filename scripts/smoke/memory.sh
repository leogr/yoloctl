#!/usr/bin/env bash
# Run only in the isolated fixture: exercises real allocation, per-box OOM,
# aggregate cgroup placement, resource-only reconcile and builder recovery.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

cleanup() {
    local status=$?
    set +e
    if [[ "$status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ]]; then
        printf 'Smoke failed; isolated VM and logs retained at %s\n' "$SMOKE_ROOT" >&2
        exit "$status"
    fi
    smoke_destroy_box smoke-memory
    smoke_destroy_box smoke-memory-peer
    if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
        smoke_destroy_space || status=1
        smoke_remove_owned_root
    fi
    exit "$status"
}
trap cleanup EXIT

smoke_ensure_space
for box in smoke-memory smoke-memory-peer; do smoke_destroy_box "$box"; done
"$YOLOCTL" box create smoke-memory --space "$SMOKE_SPACE" --agent none --account none \
    --workdir-mode ephemeral --no-attach
"$YOLOCTL" box create smoke-memory-peer --space "$SMOKE_SPACE" --agent none --account none \
    --workdir-mode ephemeral --memory 256m --no-attach

python3 - <<'PY'
import json
import os
from pathlib import Path
import subprocess
import sys
import time

cli = os.environ["YOLOCTL"]
space = os.environ["SMOKE_SPACE"]
systemd = os.environ["SMOKE_BACKEND"] == "lima"
parent = "/yoloctl.slice/yoloctl-workloads.slice" if systemd else "/yoloctl-workloads"

def diagnostics():
    path = Path(os.environ["SMOKE_ROOT"]) / "memory-failure.log"
    script = f"""for f in memory.current memory.high memory.max memory.events memory.pressure; do
echo "--- $f"; cat /sys/fs/cgroup{parent}/"$f"; done
dmesg | tail -n 25
"""
    try:
        p = subprocess.run([cli, "space", "ssh", space, "--root", "--", "sh", "-c", script],
                           text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=15)
        output = p.stdout
    except (subprocess.TimeoutExpired, OSError) as e:
        output = f"Could not read memory diagnostics: {e}\n"
    path.write_text(output)
    print(f"Memory failure diagnostics: {path}\n{output}", file=sys.stderr, flush=True)

def run(*args, check=True, timeout=120, **kwargs):
    try:
        p = subprocess.run([cli, *args], text=True, stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, timeout=timeout, **kwargs)
    except subprocess.TimeoutExpired:
        diagnostics()
        raise
    if check and p.returncode:
        raise RuntimeError(f"{args}: {p.returncode}\n{p.stdout}\n{p.stderr}")
    return p

def guest(*args, **kwargs):
    return run("space", "ssh", space, "--root", "--", *args, **kwargs).stdout.strip()

def inspect(name):
    return json.loads(guest("nerdctl", "inspect", name))[0]

def group(pid):
    return guest("cat", f"/proc/{pid}/cgroup").split("0::", 1)[1].strip()

def pool():
    r = json.loads(run("box", "inspect", "smoke-memory", "--space", space, "--json").stdout)
    return r["runtime"]["workload_memory"]

def alive():
    assert "peer-alive" in run("box", "exec", "smoke-memory-peer", "--space", space,
                               "--", "/bin/echo", "peer-alive").stdout

def expect_oom(allocation):
    before = pool()["events"]["oom_kill"]
    started = time.monotonic()
    # Fatal default SIGALRM bounds the guest allocator as well as the host
    # command. Killing only the CLI could leave a throttled allocation alive.
    code = f"import signal; signal.alarm(30); data=bytearray({allocation})"
    result = run("box", "exec", "smoke-memory", "--space", space, "--", "python3", "-c",
                 code, check=False, timeout=45)
    elapsed = time.monotonic() - started
    if result.returncode == 0 or pool()["events"]["oom_kill"] <= before or elapsed >= 30:
        diagnostics()
        raise AssertionError(f"expected prompt kernel OOM, got exit {result.returncode} after {elapsed:.2f}s")
    print(f"kernel OOM observed after {elapsed:.2f}s", flush=True)
    alive()

shared = inspect("smoke-memory")
peer = inspect("smoke-memory-peer")
assert shared["HostConfig"]["Memory"] == 0, shared["HostConfig"]
assert peer["HostConfig"]["Memory"] == 256 << 20, peer["HostConfig"]
assert group(shared["State"]["Pid"]).startswith(parent + "/")
assert group(peer["State"]["Pid"]).startswith(parent + "/")
mem = pool()
assert mem["max_bytes"] >= 2600 << 20, "This gate requires a VM with at least 4 GiB"
assert mem["high_bytes"] is None, "early throttling must be disabled"
print(">>> memory: shared box can exceed the old 2 GiB limit", flush=True)
assert "allocated" in run("box", "exec", "smoke-memory", "--space", space, "--",
                           "python3", "-c", "data=bytearray(2304*1024*1024); print('allocated')").stdout
alive()

print(">>> memory: resource-only reconcile, bounded OOM and cap removal", flush=True)
run("box", "set", "smoke-memory", "--space", space, "--memory", "128m")
limited = inspect("smoke-memory")
assert limited["Id"] != shared["Id"]
assert limited["HostConfig"]["Memory"] == 128 << 20
expect_oom(512 << 20)
run("box", "set", "smoke-memory", "--space", space, "--memory", "shared")
assert inspect("smoke-memory")["HostConfig"]["Memory"] == 0

print(">>> memory: aggregate limit protects VM infrastructure", flush=True)
allocation = pool()["max_bytes"] + (256 << 20)
expect_oom(allocation)

# A policy-only feature yields a tiny FROM-only build through the public CLI.
def image_build():
    run("image", "build", "--space", space, "--tag", "yoloctl/memory-smoke:recovery",
        "--features", "pypi")

image_build()
probe = r'''
import json
from pathlib import Path
rows=[]
for p in Path('/proc').iterdir():
    if not p.name.isdigit(): continue
    try:
        args=(p/'cmdline').read_bytes().split(b'\0')
        kind=''
        if (p/'comm').read_text().strip()=='buildkitd': kind='builder'
        elif args[1:3]==[b'controller-internal',b'serve']: kind='controller'
        elif args[0].rsplit(b'/',1)[-1]==b'sleep' and args[1:2]==[b'19.123']: kind='step'
        if kind: rows.append([kind,int(p.name),(p/'cgroup').read_text().split('0::',1)[1].strip()])
    except (FileNotFoundError,ProcessLookupError): pass
print(json.dumps(rows))
'''

def processes():
    return json.loads(guest("python3", "-c", probe))

rows = processes()
assert any(k == "controller" and not g.startswith(parent + "/") for k, _, g in rows), rows
assert any(k == "builder" and g.startswith(parent + "/") for k, _, g in rows), rows
print(">>> memory: BuildKit daemon and RUN steps share the box budget", flush=True)
build_log = Path(os.environ["SMOKE_ROOT"]) / "memory-build.log"
shell = 'd=$(mktemp -d /var/tmp/yolo-memory.XXXXXX); trap \'rm -rf "$d"\' EXIT; cat > "$d/Dockerfile"; nerdctl build --no-cache -t yoloctl/memory-smoke:step "$d"'
with build_log.open("w") as log:
    build = subprocess.Popen([cli, "space", "ssh", space, "--root", "--", "sh", "-c", shell],
                             stdin=subprocess.PIPE, stdout=log, stderr=log, text=True)
    try:
        build.stdin.write("FROM yoloctl/agentbox:ubuntu-24.04\nRUN sleep 19.123\n")
        build.stdin.close()
        deadline = time.monotonic() + 60
        while True:
            steps = [g for k, _, g in processes() if k == "step"]
            if steps:
                assert all(g.startswith(parent + "/") for g in steps), steps
                break
            assert build.poll() is None, build_log.read_text()
            assert time.monotonic() < deadline, "BuildKit RUN step did not appear"
            time.sleep(0.1)
        assert build.wait(timeout=60) == 0, build_log.read_text()
    finally:
        if build.poll() is None:
            build.terminate()
            build.wait(timeout=10)

print(">>> memory: builder death retains the VM; next build can recover", flush=True)
pid = next(pid for k, pid, _ in processes() if k == "builder")
guest("kill", "-KILL", str(pid))
deadline = time.monotonic() + 10
while any(k == "builder" and p == pid for k, p, _ in processes()):
    assert time.monotonic() < deadline, "builder not reaped"
    time.sleep(0.1)
alive()
image_build()
assert any(k == "builder" and p != pid and g.startswith(parent + "/") for k, p, g in processes())
print(">>> PASS: shared memory, explicit limits, OOM isolation, resource drift and builder recovery", flush=True)
PY
