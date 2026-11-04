#!/usr/bin/env bash
# Exercise the real VM controller, in-box shim, admission and native store.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
OP=smoke-operator
PLAIN=smoke-ordinary
MADE=smoke-operated
cleanup() {
    set +e
    if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
        smoke_destroy_space
        smoke_remove_owned_root
    fi
}
trap cleanup EXIT
smoke_ensure_space
op() {
    local log status=0
    log="$(mktemp "${SMOKE_ROOT}/operator-stderr.XXXXXX")"
    "$YOLOCTL" box exec "$OP" --space "$SMOKE_SPACE" -- yoloctl "$@" 2>"$log" || status=$?
    cat "$log" >&2
    if [[ "$status" == 0 ]] && grep -q '^Error:' "$log"; then
        smoke_fail "successful helper command printed an internal transport error (saved in $log)"
    fi
    rm -f "$log"
    return "$status"
}
deny() {
    if "$@" >"${SMOKE_ROOT}/operator-denied.log" 2>&1; then
        smoke_fail "unexpectedly authorized: $*"
    fi
}

smoke_info "controller storage is native; operator grant is explicit"
"$YOLOCTL" box create "$OP" --space "$SMOKE_SPACE" --agent none --features yolo.space.operator \
    --workdir "$SMOKE_WORKDIR" --no-attach --no-autoupdate
"$YOLOCTL" box create "$PLAIN" --space "$SMOKE_SPACE" --agent none \
    --workdir "$SMOKE_WORKDIR" --no-attach --no-autoupdate
disk_name="$("$YOLOCTL" space show "$SMOKE_SPACE" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["account_disk_name"])')"
native="/mnt/lima-${disk_name}/config"
fstype="$("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- findmnt -n -o FSTYPE -T "${native}/space.yaml")"
[[ "$fstype" == ext4 ]] || smoke_fail "controller config is not on ext4"
[[ ! -e "${SMOKE_CONFIG_ROOT}/spaces/${SMOKE_SPACE}/boxes/${OP}.yaml" ]] || smoke_fail "host box declaration exists"
"$YOLOCTL" box exec "$OP" --space "$SMOKE_SPACE" -- test -r /etc/codex/skills/yolo-box-operator/SKILL.md
"$YOLOCTL" box exec "$OP" --space "$SMOKE_SPACE" -- test -r /etc/claude-code/.claude/skills/yolo-box-operator/SKILL.md
"$YOLOCTL" box exec "$OP" --space "$SMOKE_SPACE" -- test -r /home/sandbox/.agents/skills/yolo-box-operator/SKILL.md
"$YOLOCTL" box exec "$OP" --space "$SMOKE_SPACE" -- sh -ec '
    mkdir /home/sandbox/.agents/skills/yoloctl-smoke-user
    rmdir /home/sandbox/.agents/skills/yoloctl-smoke-user
    test ! -w /home/sandbox/.agents/skills/yolo-box-operator/SKILL.md
'
"$YOLOCTL" box exec "$PLAIN" --space "$SMOKE_SPACE" -- test ! -e /home/sandbox/.agents/skills/yolo-box-operator/SKILL.md
operator_diff="$("$YOLOCTL" box diff "$OP" --space "$SMOKE_SPACE")"
[[ "$operator_diff" == "PLAN: no changes" ]] || smoke_fail "operator infrastructure mounts cause drift: $operator_diff"
host_bundle="$("$YOLOCTL" version --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["bundle"])')"
helper_bundle="$(op version --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["bundle"])')"
[[ "$host_bundle" == "$helper_bundle" ]] || smoke_fail "helper and host bundle differ"

smoke_info "shared box CLI and caller-relative host paths"
op box create "$MADE" --agent none --workdir . --manifest-only --no-attach
op box show "$MADE" >"${SMOKE_ROOT}/operated.yaml"
grep -Fq "$SMOKE_WORKDIR" "${SMOKE_ROOT}/operated.yaml" || smoke_fail "relative workdir did not map to host grant"
op box copy "$MADE" "${MADE}-copy" --manifest-only --no-attach
op box start "$MADE" --no-attach --no-autoupdate
[[ "$(op box exec "$MADE" -- printf 'exact-byte-stream')" == exact-byte-stream ]] || smoke_fail "nested exec changed bytes"
op box inspect "$MADE" >/dev/null
op box diff "$MADE" >/dev/null
op box stop "$MADE"
op box start "$MADE" --no-attach --no-autoupdate
op box restart "$MADE" --no-attach --no-autoupdate
op box recreate "$MADE" --force --no-attach --no-autoupdate
op box logs "$MADE" >/dev/null
op box reconcile "$MADE"

smoke_info "concurrent host/helper declarations use one native lock domain"
"$YOLOCTL" box create smoke-host-writer --space "$SMOKE_SPACE" --agent none --workdir-mode ephemeral --manifest-only --no-attach &
host_writer=$!
op box create smoke-helper-writer --agent none --workdir-mode ephemeral --manifest-only --no-attach &
helper_writer=$!
wait "$host_writer"
wait "$helper_writer"
op box show smoke-host-writer >/dev/null
"$YOLOCTL" box show smoke-helper-writer --space "$SMOKE_SPACE" >/dev/null

smoke_info "operator cannot administer accounts/spaces or escape its scope"
deny op account list
deny op space stop "$SMOKE_SPACE"
deny op box list --space different-space
deny op box create forbidden --agent none --mount "${native}:/exposed" --manifest-only
deny "$YOLOCTL" box exec "$OP" --space "$SMOKE_SPACE" -- cat /run/yoloctl/controller/admin.sock
# Copying the helper carries no endpoint or authority.
"$YOLOCTL" box exec "$OP" --space "$SMOKE_SPACE" -- cat /opt/yoloctl/helper/yoloctl >"${SMOKE_WORKDIR}/copied-yoloctl"
chmod +x "${SMOKE_WORKDIR}/copied-yoloctl"
deny "$YOLOCTL" box exec "$PLAIN" --space "$SMOKE_SPACE" -- "/workdir${SMOKE_WORKDIR}/copied-yoloctl" box list
rm "${SMOKE_WORKDIR}/copied-yoloctl"

smoke_info "host secret environment is captured before RPC"
SMOKE_CONTROLLER_SECRET=synthetic-from-caller "$YOLOCTL" account secret set default SMOKE_CONTROLLER_SECRET --space "$SMOKE_SPACE" --from-env
"$YOLOCTL" account secret list default --space "$SMOKE_SPACE" | grep -q SMOKE_CONTROLLER_SECRET
"$YOLOCTL" account secret unset default SMOKE_CONTROLLER_SECRET --space "$SMOKE_SPACE"

smoke_info "controller restart preserves declarations and restores operator endpoints"
if [[ "$SMOKE_BACKEND" == firecracker ]]; then
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- /sbin/yoloctl-init controller stop
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- /sbin/yoloctl-init controller start
else
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- systemctl restart yoloctl-controller.service
fi
ready=0
for _ in {1..30}; do
    if op box show "$MADE" >/dev/null 2>&1; then ready=1; break; fi
    sleep 1
done
[[ "$ready" == 1 ]] || smoke_fail "operator did not reconnect after controller restart"

smoke_info "revocation removes operator infrastructure; delegation can be re-enabled explicitly"
"$YOLOCTL" box set "$OP" --space "$SMOKE_SPACE" --features ''
deny "$YOLOCTL" box exec "$OP" --space "$SMOKE_SPACE" -- /opt/yoloctl/helper/yoloctl box list
"$YOLOCTL" box set "$OP" --space "$SMOKE_SPACE" --features yolo.space.operator
op box destroy "${MADE}-copy" --force

smoke_info "stopped spaces reject declaration commands without booting"
"$YOLOCTL" space stop "$SMOKE_SPACE" 2>"${SMOKE_ROOT}/operator-stop.log"
cat "${SMOKE_ROOT}/operator-stop.log" >&2
if grep -Eq '^Error:|warning: (controller drain unavailable|workload cleanup failed|graceful shutdown failed)' "${SMOKE_ROOT}/operator-stop.log"; then
    smoke_fail "space stop required error recovery; inspect operator-stop.log"
fi
deny "$YOLOCTL" box list --space "$SMOKE_SPACE"
grep -q 'space start' "${SMOKE_ROOT}/operator-denied.log" || smoke_fail "offline error has no start hint"
"$YOLOCTL" space show "$SMOKE_SPACE" --json | python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["declarations_available"] is False'
smoke_assert_lima_globals_absent
"$YOLOCTL" space start "$SMOKE_SPACE"
"$YOLOCTL" box start "$OP" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
op box show "$MADE" >/dev/null

smoke_info "VM replacement retains controller configuration and requires explicit box starts"
smoke_assert_lima_globals_absent
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
"$YOLOCTL" box show "$MADE" --space "$SMOKE_SPACE" >/dev/null
"$YOLOCTL" box start "$OP" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
op box show smoke-helper-writer >/dev/null
smoke_info "PASS: controller persistence, operator scope, transport, concurrency and revocation"
