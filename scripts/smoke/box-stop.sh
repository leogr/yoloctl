#!/usr/bin/env bash
# Isolated stop/recreate regression: TERM reaches the supervised workload,
# shutdown stays below the runtime's 30-second escalation, and peers survive.
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
    smoke_destroy_box smoke-stop
    smoke_destroy_box smoke-stop-peer
    if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
        smoke_destroy_space || status=1
        smoke_remove_owned_root
    fi
    exit "$status"
}
trap cleanup EXIT

smoke_ensure_space
for box in smoke-stop smoke-stop-peer; do
    smoke_destroy_box "$box"
    "$YOLOCTL" box create "$box" --space "$SMOKE_SPACE" --agent none --account none \
        --workdir-mode ephemeral --memory 256m --no-attach
done

smoke_info "confined shutdown: repeated stop/start and cached recreate"
python3 - <<'PY'
import json
import os
import subprocess
import time

cli = os.environ["YOLOCTL"]
space = os.environ["SMOKE_SPACE"]

def run(*args):
    return subprocess.run([cli, *args], check=True, text=True, stdout=subprocess.PIPE).stdout

def timed(*args):
    start = time.monotonic()
    run(*args)
    elapsed = time.monotonic() - start
    print(f"{' '.join(args)}: {elapsed:.3f}s", flush=True)
    # This gate catches the observed fixed 30s wait, not small machine-to-machine
    # performance variation. Container exit status separately proves TERM.
    assert elapsed < 15, f"routine teardown reached the escalation delay: {elapsed:.3f}s"

for _ in range(3):
    timed("box", "stop", "smoke-stop", "--space", space)
    state = json.loads(run("space", "ssh", space, "--root", "--", "nerdctl", "inspect",
                           "--format", "{{json .State}}", "smoke-stop"))
    assert state["Running"] is False and state["ExitCode"] == 143, state
    assert "peer-alive" in run("box", "exec", "smoke-stop-peer", "--space", space,
                                "--", "/bin/echo", "peer-alive")
    timed("box", "start", "smoke-stop", "--space", space, "--no-attach")
timed("box", "recreate", "smoke-stop", "--space", space, "--force", "--no-attach")
assert "ready" in run("box", "exec", "smoke-stop", "--space", space, "--", "/bin/echo", "ready")
PY
smoke_info "PASS: graceful confined stop, cached recreate and unaffected peer"
