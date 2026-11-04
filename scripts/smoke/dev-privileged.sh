#!/usr/bin/env bash
# Disposable-host runtime gate for privileged boxes (--privileged). It creates
# its own space, compares a confined and a privileged box, and removes
# everything on exit. A privileged box is space-VM root: run this only on a disposable host
# with a separately authorized space, never in a coding VM or inside a box.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

[[ "${SMOKE_DEV_PRIVILEGED_CONFIRM:-}" == 1 ]] ||
    smoke_fail "set SMOKE_DEV_PRIVILEGED_CONFIRM=1 on a disposable host to run the privileged-box gate"
[[ -z "${OPENSHELL_SANDBOX:-}" && ! -e /run/openshell && ! -e /opt/yoloctl/helper ]] ||
    smoke_fail "refusing to run inside a yoloctl box"
smoke_init

CONF=smoke-confined
PRIV=smoke-privileged
OP=smoke-priv-operator
OPMADE=smoke-op-made
PORT="${SMOKE_PRIVILEGED_PORT:-18471}"
RESULTS=()
CURRENT=""

phase() {
    CURRENT="$1"
    smoke_info "$1"
}
pass() {
    RESULTS+=("PASS  $CURRENT")
    CURRENT=""
}
cleanup() {
    local status=$?
    set +e
    [[ -n "$CURRENT" ]] && RESULTS+=("FAIL  $CURRENT")
    printf '\n%s\n' "privileged-box gate results:"
    printf '  %s\n' "${RESULTS[@]}"
    if [[ "$status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ]]; then
        printf 'Smoke failed; isolated VM and logs retained at %s\n' "$SMOKE_ROOT" >&2
        exit "$status"
    fi
    for box in "$CONF" "$PRIV" "$OP" "$OPMADE"; do
        smoke_destroy_box "$box"
    done
    if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
        smoke_destroy_space || status=1
        smoke_remove_owned_root
    fi
    exit "$status"
}
trap cleanup EXIT

in_box() {
    local box="$1"
    shift
    "$YOLOCTL" box exec "$box" --space "$SMOKE_SPACE" -- "$@"
}
expect_failure() {
    local log="${SMOKE_ROOT}/expected-failure.log"
    if "$@" >"$log" 2>&1; then
        smoke_fail "unexpectedly succeeded: $*"
    fi
    cat "$log"
}

smoke_ensure_space
host_uid="$(id -u)"

phase "explicit opt-in creates a privileged box beside a confined one"
for box in "$CONF" "$PRIV"; do
    smoke_destroy_box "$box"
done
refused="$(expect_failure "$YOLOCTL" box create "$PRIV" --space "$SMOKE_SPACE" --agent none --account none \
    --workdir-mode ephemeral --capability yolo.dev.privileged --manifest-only)"
grep -q -- '--privileged' <<<"$refused" || smoke_fail "the retired capability was not refused with guidance"
"$YOLOCTL" box create "$CONF" --space "$SMOKE_SPACE" --agent none --account none \
    --workdir-mode ephemeral --memory 256m --no-attach
"$YOLOCTL" box create "$PRIV" --space "$SMOKE_SPACE" --agent none --account none \
    --workdir-mode ephemeral --memory 256m --privileged \
    --port "${PORT}:8080" --no-attach
[[ "$("$YOLOCTL" box diff "$PRIV" --space "$SMOKE_SPACE")" == "PLAN: no changes" ]] ||
    smoke_fail "a fresh privileged box reports drift"
pass

phase "runtime evidence: privileged record, endpoint launch, labels and views"
python3 - "$CONF" "$PRIV" <<'PY'
import json, os, subprocess, sys
cli, space = os.environ["YOLOCTL"], os.environ["SMOKE_SPACE"]
conf, priv = sys.argv[1:3]
def inspect(box):
    out = subprocess.run([cli, "space", "ssh", space, "--root", "--", "nerdctl", "inspect", box],
                         check=True, text=True, stdout=subprocess.PIPE).stdout
    return json.loads(out)[0]
p, c = inspect(priv), inspect(conf)
assert p["HostConfig"]["Privileged"] is True and c["HostConfig"]["Privileged"] is False, (p["HostConfig"], c["HostConfig"])
assert p["Args"][:2] == ["/opt/yoloctl/bin/yoloctl", "box-endpoint-internal"], p["Args"]
assert p["Config"]["Labels"]["yoloctl.runtime_mode"] == "privileged", p["Config"]["Labels"]
assert "yoloctl.runtime_mode" not in c["Config"]["Labels"], c["Config"]["Labels"]
assert any(m["Destination"] == "/opt/yoloctl/bin" and not m.get("RW", True) for m in p["Mounts"]), p["Mounts"]
show = json.loads(subprocess.run([cli, "box", "show", priv, "--space", space, "--json"],
                                 check=True, text=True, stdout=subprocess.PIPE).stdout)
assert show["privileged"] is True and show["runtime_mode"] == "privileged", show
PY
pass

phase "confinement differs: confined box keeps OpenShell layers, privileged box has sudo root"
in_box "$CONF" sh -c 'grep -qx "NoNewPrivs:[[:space:]]*1" /proc/self/status && grep -qx "Seccomp:[[:space:]]*2" /proc/self/status'
expect_failure in_box "$CONF" sudo -n true
in_box "$PRIV" sh -c 'grep -qx "NoNewPrivs:[[:space:]]*0" /proc/self/status && grep -qx "Seccomp:[[:space:]]*0" /proc/self/status'
[[ "$(in_box "$PRIV" id -u)" == "$host_uid" ]] || smoke_fail "privileged child is not the mapped sandbox user"
[[ "$(in_box "$PRIV" sudo -n id -u)" == 0 ]] || smoke_fail "sudo does not reach root"
cap_eff="$(in_box "$PRIV" sudo -n sh -c 'sed -n "s/^CapEff:[[:space:]]*//p" /proc/self/status')"
(( (16#$cap_eff >> 16) & 1 )) || smoke_fail "root lacks CAP_SYS_MODULE: $cap_eff"
# Confined enumeration may be denied: wc then reports zero visible entries.
# This is not a claim that the confined /dev contains no device nodes.
conf_devices="$(in_box "$CONF" sh -c 'ls /dev | wc -l')"
priv_devices="$(in_box "$PRIV" sh -c 'ls /dev | wc -l')"
(( priv_devices > conf_devices )) || smoke_fail "privileged box shows no extra VM devices ($priv_devices <= $conf_devices)"
in_box "$PRIV" sudo -n test -r /proc/modules
pass

phase "environment and native ownership match the confined launch"
env_dump="$(in_box "$PRIV" env)"
grep -qx 'HOME=/home/sandbox' <<<"$env_dump" && grep -qx 'USER=sandbox' <<<"$env_dump" ||
    smoke_fail "child HOME/USER differ"
! grep -q 'OPENSHELL_' <<<"$env_dump" || smoke_fail "child sees OpenShell variables"
[[ "$(in_box "$PRIV" sh -c 'touch /workdir/owned && stat -c %u /workdir/owned /home/sandbox | sort -u')" == "$host_uid" ]] ||
    smoke_fail "workdir or home is not owned by the mapped user"
pass

phase "managed session through the holder"
python3 "$(dirname "${BASH_SOURCE[0]}")/privileged-session.py" \
    --yoloctl "$YOLOCTL" --space "$SMOKE_SPACE" --box "$PRIV" \
    --output "${SMOKE_ROOT}/privileged-holder"
pass

phase "port forward reaches a listener in the container namespace"
python3 "$(dirname "${BASH_SOURCE[0]}")/privileged-runtime.py" port \
    --yoloctl "$YOLOCTL" --space "$SMOKE_SPACE" --box "$PRIV" --port "$PORT" \
    --output "${SMOKE_ROOT}/privileged-port"
pass

phase "stop ends root descendants within the runtime grace"
python3 "$(dirname "${BASH_SOURCE[0]}")/privileged-runtime.py" root-ready \
    --yoloctl "$YOLOCTL" --space "$SMOKE_SPACE" --box "$PRIV" \
    --output "${SMOKE_ROOT}/privileged-root-descendant"
in_box "$PRIV" pgrep -u 0 -x sleep >/dev/null || smoke_fail "root descendant did not start"
start="$(date +%s)"
"$YOLOCTL" box stop "$PRIV" --space "$SMOKE_SPACE"
(( $(date +%s) - start < 25 )) || smoke_fail "stop reached the runtime escalation"
"$YOLOCTL" box start "$PRIV" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
expect_failure in_box "$PRIV" pgrep -x sleep
pass

phase "operator authority: a running privileged box is operable, only the host starts one"
smoke_destroy_box "$OP"
"$YOLOCTL" box create "$OP" --space "$SMOKE_SPACE" --agent none --features yolo.space.operator \
    --workdir-mode ephemeral --memory 256m --no-attach --no-autoupdate
in_box "$OP" yoloctl box show "$PRIV" >/dev/null
[[ "$(in_box "$OP" yoloctl box exec "$CONF" -- echo operable)" == operable ]] ||
    smoke_fail "operator cannot act on its ordinary confined peer"
[[ "$(in_box "$OP" yoloctl box exec "$PRIV" -- sudo -n id -u)" == 0 ]] ||
    smoke_fail "operator cannot use the running privileged box"
# Restarting the running box continues the host-started runtime.
in_box "$OP" yoloctl box restart "$PRIV" --no-attach --no-autoupdate
[[ "$(in_box "$OP" yoloctl box exec "$PRIV" -- sudo -n id -u)" == 0 ]] ||
    smoke_fail "operator restart did not continue the privileged runtime"
in_box "$OP" yoloctl box stop "$PRIV"
refused="$(expect_failure in_box "$OP" yoloctl box start "$PRIV" --no-attach --no-autoupdate)"
grep -q 'only the host starts it' <<<"$refused" || smoke_fail "operator start of a stopped privileged box"
refused="$(expect_failure in_box "$OP" yoloctl box create "$OPMADE" --agent none --workdir-mode ephemeral \
    --privileged --no-attach)"
grep -q -- '--manifest-only' <<<"$refused" || smoke_fail "operator create did not name --manifest-only"
in_box "$OP" yoloctl box create "$OPMADE" --agent none --workdir-mode ephemeral \
    --privileged --manifest-only
refused="$(expect_failure in_box "$OP" yoloctl box reconcile "$OPMADE")"
grep -q 'only the host starts it' <<<"$refused" || smoke_fail "operator materialized a privileged declaration"
in_box "$OP" yoloctl box destroy "$OPMADE" --force
started="$("$YOLOCTL" box start "$PRIV" --space "$SMOKE_SPACE" --no-attach --no-autoupdate 2>&1)"
grep -q 'every yolo.space.operator box in this space' <<<"$started" ||
    smoke_fail "host start did not warn about operator access"
pass

phase "mode changes need an explicit recreate, both ways"
refused="$(expect_failure "$YOLOCTL" box set "$CONF" --space "$SMOKE_SPACE" --privileged)"
grep -q 'box recreate' <<<"$refused" || smoke_fail "set did not require an explicit recreate"
"$YOLOCTL" box recreate "$CONF" --space "$SMOKE_SPACE" --force --no-attach
[[ "$(in_box "$CONF" sudo -n id -u)" == 0 ]] || smoke_fail "confined -> privileged recreate"
editor="${SMOKE_ROOT}/drop-privileged-editor"
printf '%s\n' '#!/bin/sh' "sed -i '/^privileged:/d' \"\$1\"" >"$editor"
chmod 0755 "$editor"
export EDITOR="$editor"
refused="$(expect_failure "$YOLOCTL" box edit "$PRIV" --space "$SMOKE_SPACE")"
unset EDITOR
grep -q 'box recreate' <<<"$refused" || smoke_fail "edit did not require an explicit recreate"
"$YOLOCTL" box recreate "$PRIV" --space "$SMOKE_SPACE" --force --no-attach
expect_failure in_box "$PRIV" sudo -n true
in_box "$PRIV" sh -c 'grep -qx "NoNewPrivs:[[:space:]]*1" /proc/self/status'
pass

smoke_info "PASS: privileged-box gate"
