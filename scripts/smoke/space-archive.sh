#!/usr/bin/env bash
# Portable backups and duplication through the normal isolated CLI lifecycle.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
IMPORT_BACKEND="${SMOKE_IMPORT_BACKEND:-$SMOKE_BACKEND}"
smoke_require_backend "$IMPORT_BACKEND"
IMPORT_VM_ARGS=(--backend "$IMPORT_BACKEND")
if [[ "$IMPORT_BACKEND" == firecracker && -n "${SMOKE_VM_IMAGE:-}" ]]; then
    IMPORT_VM_ARGS+=(--vm-image "$SMOKE_VM_IMAGE")
fi
RESTORED="${SMOKE_SPACE}-restored"
ARCHIVE="${SMOKE_ROOT}/space-backup.tar.gz"
ACCOUNT=archive-fixture
BOX=archive-declaration
cleanup() {
    local status=$?
    set +e
    if [[ "$status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ]]; then
        printf 'Archive smoke failed; isolated spaces and logs retained at %s\n' "$SMOKE_ROOT" >&2
        exit "$status"
    fi
    if ! "$YOLOCTL" space destroy "$RESTORED" --force >/dev/null 2>&1; then
        SMOKE_CLEANUP_FAILED=1
        status=1
        printf 'Cleanup incomplete; isolated recovery state retained at %s\n' "$SMOKE_ROOT" >&2
    fi
    if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
        smoke_destroy_space || status=1
        smoke_remove_owned_root
    fi
    if [[ "$status" == 0 ]]; then
        smoke_info 'PASS: archive round trip, private backup, remapped workdir, independent identity/state, recreate and stopped-space export'
    fi
    exit "$status"
}
trap cleanup EXIT
smoke_ensure_space
smoke_destroy_box "$BOX"
smoke_destroy_account "$ACCOUNT"
"$YOLOCTL" account create "$ACCOUNT" --space "$SMOKE_SPACE"
printf '%s\n' '{"synthetic":"archive native sentinel"}' >"${SMOKE_ROOT}/archive-auth.json"
"$YOLOCTL" account auth import "$ACCOUNT" codex --space "$SMOKE_SPACE" --file "${SMOKE_ROOT}/archive-auth.json"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent none --account none \
    --workdir "$SMOKE_WORKDIR" --manifest-only
native="/var/lib/yoloctl/account-state/${ACCOUNT}/codex/auth.json"
native_leaf="${native%/auth.json}"
smoke_info 'archive: native daemon links, sockets and helper scratch state'
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- python3 - "$native_leaf" <<'PY'
import pathlib, socket, sys
p = pathlib.Path(sys.argv[1])
for name in ('packages/app-server-daemon/releases/fixture', 'app-server-control',
             'app-server-daemon', 'tmp/arg0/codex-fixture', 'sessions'):
    (p / name).mkdir(parents=True, exist_ok=True)
(p / 'packages/app-server-daemon/releases/fixture/codex').write_text('fixture executable')
(p / 'packages/app-server-daemon/current').symlink_to(
    '/home/sandbox/.codex/packages/app-server-daemon/releases/fixture')
(p / 'app-server-control/app-server-control.sock').symlink_to('/tmp/codex-daemon-fixture/control')
(p / 'tmp/arg0/codex-fixture/apply_patch').symlink_to('/home/sandbox/.npm-global/bin/codex')
(p / 'app-server-daemon/daemon.pid').write_text('12345')
(p / 'app-server-daemon/settings.json').write_text('{"fixture":true}')
(p / 'sessions/history.jsonl').write_text('saved history fixture\n')
s = socket.socket(socket.AF_UNIX)
s.bind(str(p / 'app-server-daemon/daemon-updater.sock'))
s.close()  # Leave a stale socket, as stopping a container can do.
(p / 'unsupported-external-link').symlink_to('/etc/../passwd')
PY
if "$YOLOCTL" space export "$SMOKE_SPACE" "${SMOKE_ROOT}/unsafe-backup.tar.gz" --stop >"${SMOKE_ROOT}/unsafe-export.log" 2>&1; then
    smoke_fail 'export accepted an unclean absolute link'
fi
[[ ! -e "${SMOKE_ROOT}/unsafe-backup.tar.gz" ]] || smoke_fail 'failed export published an archive'
grep -F "accounts/${ACCOUNT}/codex/unsupported-external-link" "${SMOKE_ROOT}/unsafe-export.log" >/dev/null || smoke_fail 'export error did not name the rejected entry'
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- rm -- "${native_leaf}/unsupported-external-link"
before="$("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- sha256sum "$native")"
source_uuid="$("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- findmnt -n -o UUID --mountpoint /var/lib/yoloctl/account-state)"
"$YOLOCTL" space export "$SMOKE_SPACE" "$ARCHIVE" --stop
"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- python3 - "$native_leaf" <<'PY'
import os, pathlib, stat, sys
p = pathlib.Path(sys.argv[1])
assert os.readlink(p / 'packages/app-server-daemon/current') == '/home/sandbox/.codex/packages/app-server-daemon/releases/fixture'
assert os.readlink(p / 'app-server-control/app-server-control.sock') == '/tmp/codex-daemon-fixture/control'
assert (p / 'tmp/arg0/codex-fixture/apply_patch').is_symlink()
assert (p / 'app-server-daemon/daemon.pid').read_text() == '12345'
assert stat.S_ISSOCK((p / 'app-server-daemon/daemon-updater.sock').lstat().st_mode)
PY
if "$YOLOCTL" space export "$SMOKE_SPACE" "$ARCHIVE" --stop >"${SMOKE_ROOT}/overwrite.log" 2>&1; then
    smoke_fail 'export overwrote an existing backup'
fi
python3 - "$ARCHIVE" <<'PY'
import os, stat, sys, tarfile
p=sys.argv[1]
assert stat.S_IMODE(os.stat(p).st_mode)==0o600
with tarfile.open(p,'r:gz') as t:
    assert t.getnames()==['manifest.json','state.tar']
PY
mkdir -p "${SMOKE_ROOT}/restored-project"
"$YOLOCTL" space import "$RESTORED" "$ARCHIVE" "${IMPORT_VM_ARGS[@]}" --workdir "${SMOKE_ROOT}/restored-project"
"$YOLOCTL" account show "$ACCOUNT" --space "$RESTORED" >/dev/null
"$YOLOCTL" box show "$BOX" --space "$RESTORED" >/dev/null
after="$("$YOLOCTL" space ssh "$RESTORED" --root -- sha256sum "$native")"
[[ "$before" == "$after" ]] || smoke_fail 'native account bytes changed during restore'
"$YOLOCTL" space ssh "$RESTORED" --root -- python3 - "$native_leaf" <<'PY'
import os, pathlib, sys
p = pathlib.Path(sys.argv[1])
assert os.readlink(p / 'packages/app-server-daemon/current') == 'releases/fixture'
assert (p / 'packages/app-server-daemon/current/codex').read_text() == 'fixture executable'
assert (p / 'app-server-daemon/settings.json').read_text() == '{"fixture":true}'
assert (p / 'sessions/history.jsonl').read_text() == 'saved history fixture\n'
for name in ('tmp/arg0', 'app-server-control/app-server-control.sock',
             'app-server-daemon/daemon.pid', 'app-server-daemon/daemon-updater.sock'):
    assert not os.path.lexists(p / name), f'runtime artifact restored: {name}'
PY
restored_uuid="$("$YOLOCTL" space ssh "$RESTORED" --root -- findmnt -n -o UUID --mountpoint /var/lib/yoloctl/account-state)"
[[ -n "$restored_uuid" && "$source_uuid" != "$restored_uuid" ]] || smoke_fail 'restored space reused source disk identity'
"$YOLOCTL" space recreate "$RESTORED" --force
[[ "$before" == "$("$YOLOCTL" space ssh "$RESTORED" --root -- sha256sum "$native")" ]] || smoke_fail 'restored native state did not survive recreate'
"$YOLOCTL" space ssh "$RESTORED" --root -- python3 -c 'import pathlib,sys; pathlib.Path(sys.argv[1]).write_text("independent destination")' "$native"
[[ "$before" == "$("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- sha256sum "$native")" ]] || smoke_fail 'duplicate changed source account state'
"$YOLOCTL" space stop "$RESTORED"
"$YOLOCTL" space export "$RESTORED" "${SMOKE_ROOT}/stopped-backup.tar.gz"
if "$YOLOCTL" account list --space "$RESTORED" >"${SMOKE_ROOT}/offline-read.log" 2>&1; then
    smoke_fail 'export left a previously stopped VM running'
fi
smoke_destroy_box "$BOX"
smoke_destroy_account "$ACCOUNT"
