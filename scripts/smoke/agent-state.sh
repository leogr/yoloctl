#!/usr/bin/env bash
# Account state stays on a space-owned native disk through box/VM replacement.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

BOX="smoke-account-state"
ACCOUNT="smoke-state"
SECOND_SPACE="${SMOKE_SPACE}-b"
SECOND_SPACE_DESTROYED=0
SECOND_BOX="smoke-account-state-b"
CONTAINER_SENTINEL="/home/sandbox/.claude/yoloctl-smoke-state"
NATIVE_ROOT="/var/lib/yoloctl/account-state"
HISTORY_ROOT="/home/sandbox/.claude/yoloctl-smoke-history"

history_probe() {
	local box="$1" action="$2" count="$3"
	"$YOLOCTL" box exec "$box" --space "$SMOKE_SPACE" -- \
		python3 - "$action" "$HISTORY_ROOT" "$count" \
		< "$(dirname "${BASH_SOURCE[0]}")/native-large-append.py"
}

assert_state() {
	local box="$1" space="$2" expected="$3" checkpoint="$4" content
	if ! content="$("$YOLOCTL" box exec "$box" --space "$space" -- cat "$CONTAINER_SENTINEL")"; then
		smoke_fail "$checkpoint: box exec could not read the state sentinel; persistence is unverified (inspect with yoloctl box logs $box --space $space)"
	fi
	[[ "$content" == "$expected" ]] || smoke_fail "$checkpoint: account state sentinel content changed"
}

cleanup() {
	local status=$?
	set +e
	if [[ "$status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ]]; then
		printf 'Account-state smoke failed; isolated spaces and logs retained at %s\n' "$SMOKE_ROOT" >&2
		exit "$status"
	fi
	if [[ "$SECOND_SPACE_DESTROYED" != 1 ]] && ! "$YOLOCTL" space destroy "$SECOND_SPACE" --force >/dev/null 2>&1; then
		SMOKE_CLEANUP_FAILED=1
		status=1
		printf 'Cleanup incomplete; isolated recovery state retained at %s\n' "$SMOKE_ROOT" >&2
	fi
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != "1" ]]; then
		smoke_destroy_space || status=1
		smoke_remove_owned_root
	fi
	if [[ "$status" == 0 ]]; then
		smoke_info "PASS: native storage, replacement preservation, space isolation and scoped destruction"
	fi
	exit "$status"
}
trap cleanup EXIT
smoke_ensure_space
smoke_destroy_box "$BOX"
smoke_destroy_account "$ACCOUNT"

smoke_info "1/5 required initial account and VM-native filesystem"
"$YOLOCTL" account show default --space "$SMOKE_SPACE" >/dev/null
fstype="$("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- findmnt -n -o FSTYPE --mountpoint "$NATIVE_ROOT")"
[[ "$fstype" == "ext4" ]] || smoke_fail "native account root is not ext4: $fstype"
[[ ! -e "${SMOKE_CONFIG_ROOT}/account-state" ]] || smoke_fail "global host state was created"
"$YOLOCTL" account create "$ACCOUNT" --space "$SMOKE_SPACE"
"$YOLOCTL" box create --no-attach "$BOX" --space "$SMOKE_SPACE" --agent claude \
	--account "$ACCOUNT" --workdir-mode ephemeral --no-autoupdate
"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- \
	sh -c "printf 'from-first\\n' > '$CONTAINER_SENTINEL'"

smoke_info "ordinary binds and grants cannot expose either native account alias"
account_disk_name="$("$YOLOCTL" space show "$SMOKE_SPACE" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["account_disk_name"])')"
disk_mount="/mnt/lima-${account_disk_name}"
forbidden_paths=(/mnt "$disk_mount" "${disk_mount}/account-state/${ACCOUNT}/claude" "$NATIVE_ROOT")
canonical_native_root="$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$NATIVE_ROOT")"
if [[ "$canonical_native_root" != "$NATIVE_ROOT" ]]; then
    forbidden_paths+=("$canonical_native_root")
fi
for forbidden in "${forbidden_paths[@]}"; do
    if "$YOLOCTL" space mount add "$SMOKE_SPACE" "$forbidden" --yes >"${SMOKE_ROOT}/rejected-mount.log" 2>&1; then
        smoke_fail "granted managed account path: $forbidden"
    fi
    grep -q 'intersects.*managed' "${SMOKE_ROOT}/rejected-mount.log" || smoke_fail "grant failed for an unrelated reason"
    if "$YOLOCTL" box create forbidden-bind --space "$SMOKE_SPACE" --agent none --workdir-mode ephemeral \
        --mount "${forbidden}:/exposed" --manifest-only >"${SMOKE_ROOT}/rejected-bind.log" 2>&1; then
        smoke_fail "bound managed account path: $forbidden"
    fi
    grep -q 'intersects.*managed' "${SMOKE_ROOT}/rejected-bind.log" || smoke_fail "bind failed for an unrelated reason"
    if "$YOLOCTL" box create forbidden-workdir --space "$SMOKE_SPACE" --agent none \
        --workdir "$forbidden" --manifest-only >"${SMOKE_ROOT}/rejected-workdir.log" 2>&1; then
        smoke_fail "used managed account path as workdir: $forbidden"
    fi
    grep -q 'intersects.*managed' "${SMOKE_ROOT}/rejected-workdir.log" || smoke_fail "workdir failed for an unrelated reason"
done

smoke_info "native mutations coexist with a running generic shell"
SHELL_BOX="${BOX}-shell"
IMPORT_ACCOUNT="smoke-import-unused"
"$YOLOCTL" box create "$SHELL_BOX" --space "$SMOKE_SPACE" --agent none --workdir-mode ephemeral --no-attach --no-autoupdate
"$YOLOCTL" account create "$IMPORT_ACCOUNT" --space "$SMOKE_SPACE"
printf '%s\n' '{"smoke":"synthetic"}' >"${SMOKE_ROOT}/synthetic-auth.json"
"$YOLOCTL" account auth import "$IMPORT_ACCOUNT" codex --space "$SMOKE_SPACE" --file "${SMOKE_ROOT}/synthetic-auth.json"
"$YOLOCTL" account destroy "$IMPORT_ACCOUNT" --space "$SMOKE_SPACE" --force
"$YOLOCTL" box exec "$SHELL_BOX" --space "$SMOKE_SPACE" -- true || smoke_fail "unrelated generic shell stopped"
smoke_destroy_box "$SHELL_BOX"

smoke_info "same-space sharing and independent accounts"
PEER="${BOX}-peer"
"$YOLOCTL" box create --no-attach "$PEER" --space "$SMOKE_SPACE" --agent claude \
 --account "$ACCOUNT" --workdir-mode ephemeral --no-autoupdate
assert_state "$PEER" "$SMOKE_SPACE" from-first "same-account sharing"
"$YOLOCTL" box create --no-attach "${BOX}-default" --space "$SMOKE_SPACE" --agent claude \
 --workdir-mode ephemeral --no-autoupdate
"$YOLOCTL" box exec "${BOX}-default" --space "$SMOKE_SPACE" -- test ! -e "$CONTAINER_SENTINEL" ||
 smoke_fail "different accounts share state in one VM"
smoke_destroy_box "${BOX}-default"

smoke_info "append, advisory locks, shared mappings and SQLite WAL on native state"
PROBE_ROOT="/home/sandbox/.claude/synthetic-storage-contract"
"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- mkdir "$PROBE_ROOT"
ENDPOINTS="${SMOKE_ROOT}/native-storage-endpoints.json"
python3 - "$ENDPOINTS" "$YOLOCTL" "$SMOKE_SPACE" "$BOX" "$PEER" "$PROBE_ROOT" <<'PYEND'
import json
import sys
path, cli, space, first, peer, root = sys.argv[1:]
with open(path, 'w') as output:
    json.dump([dict(name=name, root=root, prefix=[cli, 'box', 'exec', box, '--space', space, '--'])
               for name, box in [('a', first), ('b', peer)]], output)
PYEND
python3 "$(dirname "${BASH_SOURCE[0]}")/../experiments/storage-contract.py" --endpoints "$ENDPOINTS"
"$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- rmdir "$PROBE_ROOT"
smoke_info "large synthetic history survives exit and resumes in a fresh process"
history_probe "$BOX" init 0
history_probe "$PEER" verify 8
history_probe "$BOX" resume 8
history_probe "$PEER" verify 16
smoke_destroy_box "$PEER"

smoke_info "2/5 box replacement preserves native state"
"$YOLOCTL" box recreate "$BOX" --space "$SMOKE_SPACE" --force --no-attach --no-autoupdate
assert_state "$BOX" "$SMOKE_SPACE" from-first "box recreate"
history_probe "$BOX" verify 16
history_probe "$BOX" resume 16

smoke_info "3/5 VM replacement preserves native state and declarations"
manifest="${SMOKE_CONFIG_ROOT}/spaces/${SMOKE_SPACE}/space.yaml"
disk_id_before="$(sed -n 's/^disk_id: //p' "$manifest")"
disk_uuid_before="$(sed -n 's/^disk_uuid: //p' "$manifest")"
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
[[ "$(sed -n 's/^disk_id: //p' "$manifest")" == "$disk_id_before" ]] || smoke_fail "disk identity changed"
[[ "$(sed -n 's/^disk_uuid: //p' "$manifest")" == "$disk_uuid_before" ]] || smoke_fail "filesystem identity changed"
"$YOLOCTL" box show "$BOX" --space "$SMOKE_SPACE" >/dev/null || smoke_fail "box manifest lost"
[[ ! -e "${SMOKE_CONFIG_ROOT}/spaces/${SMOKE_SPACE}/boxes/${BOX}.yaml" ]] || smoke_fail "host declaration was recreated"
"$YOLOCTL" box start "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
assert_state "$BOX" "$SMOKE_SPACE" from-first "space recreate"
history_probe "$BOX" verify 24
history_probe "$BOX" resume 24
history_probe "$BOX" verify 32

smoke_info "4/5 the same account name in another VM is independent"
smoke_create_space "$SECOND_SPACE" --workdir "$SMOKE_WORKDIR" --account "$ACCOUNT"
"$YOLOCTL" box create --no-attach "$SECOND_BOX" --space "$SECOND_SPACE" --agent claude \
	--workdir-mode ephemeral --no-autoupdate
"$YOLOCTL" box exec "$SECOND_BOX" --space "$SECOND_SPACE" -- test ! -e "$CONTAINER_SENTINEL" ||
	smoke_fail "second space can read first space's account state"
"$YOLOCTL" box exec "$SECOND_BOX" --space "$SECOND_SPACE" -- \
	sh -c "printf 'from-second\\n' > '$CONTAINER_SENTINEL'"
assert_state "$BOX" "$SMOKE_SPACE" from-first "cross-space isolation"

smoke_info "5/5 scoped account destruction and full space destruction"
smoke_destroy_box "$BOX"
"$YOLOCTL" account destroy "$ACCOUNT" --space "$SMOKE_SPACE" --force
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- test ! -e "${NATIVE_ROOT}/${ACCOUNT}" ||
	smoke_fail "account destroy left native state"
assert_state "$SECOND_BOX" "$SECOND_SPACE" from-second "scoped account destruction"
second_manifest="${SMOKE_CONFIG_ROOT}/spaces/${SECOND_SPACE}/space.yaml"
second_disk="$("$YOLOCTL" space show "$SECOND_SPACE" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["account_disk_name"])')"
"$YOLOCTL" space destroy "$SECOND_SPACE" --force
SECOND_SPACE_DESTROYED=1
[[ ! -e "${SMOKE_CONFIG_ROOT}/lima/_disks/${second_disk}" ]] || smoke_fail "space destroy retained disk"
[[ ! -e "$second_manifest" ]] || smoke_fail "space destroy retained manifests"
[[ -d "$SMOKE_WORKDIR" ]] || smoke_fail "host project was deleted"
