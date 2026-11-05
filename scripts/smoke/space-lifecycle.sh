#!/usr/bin/env bash
# Live-VM smoke for space mount and destroy lifecycle semantics:
#
#   1. Declining a required mount restart changes neither config nor host FS.
#   2. Accepted add/remove operations restart a running VM and are live on
#      successful return; no pending manual restart remains.
#   3. Recreate preserves the space bundle, while destroy removes it.
#   4. Host workdirs survive either operation.

set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init

EXTRA_MOUNT="${SMOKE_ROOT}/extra-mount"
DECLINED_LOG="${SMOKE_ROOT}/mount-declined.log"

cleanup() {
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != "1" ]]; then
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT

SPACE_MANIFEST="${SMOKE_CONFIG_ROOT}/spaces/${SMOKE_SPACE}/space.yaml"
if [[ ! -f "$SPACE_MANIFEST" && "$SMOKE_BACKEND" == lima ]]; then
    smoke_info "recovering allocation interrupted before the first VM boot"
    SMOKE_REAL_LIMACTL="$(command -v limactl)"
    export SMOKE_REAL_LIMACTL
    boot_wrapper="${SMOKE_ROOT}/first-boot-bin"
    mkdir -p "$boot_wrapper"
    cat >"${boot_wrapper}/limactl" <<'FIRST_BOOT'
#!/bin/sh
if [ "$1" = start ]; then
    echo 'synthetic interruption before first boot' >&2
    exit 55
fi
exec "$SMOKE_REAL_LIMACTL" "$@"
FIRST_BOOT
    chmod +x "${boot_wrapper}/limactl"
    if PATH="${boot_wrapper}:$PATH" smoke_create_space "$SMOKE_SPACE" --workdir "$SMOKE_WORKDIR" --account default >"${SMOKE_ROOT}/first-boot.log" 2>&1; then
        smoke_fail "first boot was not interrupted"
    fi
    grep -q 'synthetic interruption' "${SMOKE_ROOT}/first-boot.log" || smoke_fail "creation failed before the injected interruption"
    grep -q 'initializing: true' "$SPACE_MANIFEST" || smoke_fail "lost initialization state"
fi
smoke_ensure_space
! grep -q 'initializing: true' "$SPACE_MANIFEST" || smoke_fail "initialization authority survived successful boot"
grep -q '^disk_uuid:' "$SPACE_MANIFEST" || smoke_fail "initialization did not record filesystem identity"

smoke_info "declining a mount add leaves config and host filesystem unchanged"
if printf 'n\n' |
	"$YOLOCTL" space mount add "$SMOKE_SPACE" "$EXTRA_MOUNT" >"$DECLINED_LOG" 2>&1; then
	smoke_fail "declined space mount add unexpectedly succeeded"
fi
grep -q "nothing changed" "$DECLINED_LOG" ||
	smoke_fail "declined mount add did not report that nothing changed"
[[ ! -e "$EXTRA_MOUNT" ]] ||
	smoke_fail "declined mount add created the host path"
if "$YOLOCTL" space mount list "$SMOKE_SPACE" | grep -Fq "$EXTRA_MOUNT"; then
	smoke_fail "declined mount add persisted a grant"
fi

smoke_info "mount add restarts the running space and returns with the mount live"
"$YOLOCTL" space mount add "$SMOKE_SPACE" "$EXTRA_MOUNT" --yes
"$YOLOCTL" space ssh "$SMOKE_SPACE" -- mountpoint -q "$EXTRA_MOUNT" ||
	smoke_fail "accepted mount add returned before the mount was live"
"$YOLOCTL" space ssh "$SMOKE_SPACE" -- touch "${EXTRA_MOUNT}/from-vm"
[[ -f "${EXTRA_MOUNT}/from-vm" ]] ||
	smoke_fail "VM write through the added mount did not reach the host"

smoke_info "host shares: creation modes, open handles, fsync and atomic replacement"
"$YOLOCTL" space ssh "$SMOKE_SPACE" -- python3 - "$EXTRA_MOUNT" "$SMOKE_BACKEND" < "$(dirname "${BASH_SOURCE[0]}")/vm-shares.py"
python3 - "$EXTRA_MOUNT" <<'PY'
import hashlib, pathlib, stat, sys
root = pathlib.Path(sys.argv[1])
assert stat.S_IMODE((root/'private').stat().st_mode) == 0o700
assert (root/'private/before').read_bytes() == b'replacement'
assert hashlib.sha256((root/'durable-content').read_bytes()).hexdigest() == (root/'expected.sha256').read_text()
PY

smoke_info "mount remove restarts the running space and returns with the mount absent"
"$YOLOCTL" space mount remove "$SMOKE_SPACE" "$EXTRA_MOUNT" --yes
if "$YOLOCTL" space mount list "$SMOKE_SPACE" | grep -Fq "$EXTRA_MOUNT"; then
	smoke_fail "mount remove left the grant in space config"
fi
if "$YOLOCTL" space ssh "$SMOKE_SPACE" -- mountpoint -q "$EXTRA_MOUNT" >/dev/null 2>&1; then
	smoke_fail "mount remove returned while the mount was still live"
fi
[[ -f "${EXTRA_MOUNT}/from-vm" ]] ||
	smoke_fail "mount remove deleted host data"

smoke_info "space recreate preserves declarations"
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force
SPACE_MANIFEST="${SMOKE_CONFIG_ROOT}/spaces/${SMOKE_SPACE}/space.yaml"
[[ -f "$SPACE_MANIFEST" ]] || smoke_fail "space recreate removed declaration"
[[ -d "$SMOKE_WORKDIR" ]] || smoke_fail "space recreate removed host workdir"

if [[ "$SMOKE_BACKEND" == firecracker ]]; then
    smoke_info "VM image update: invalid media leaves the running VM intact"
    old_boot="$("$YOLOCTL" space ssh "$SMOKE_SPACE" -- cat /proc/sys/kernel/random/boot_id)"
    if "$YOLOCTL" space recreate "$SMOKE_SPACE" --force --vm-image "${SMOKE_ROOT}/missing-image" > "${SMOKE_ROOT}/image-rejected.log" 2>&1; then
        smoke_fail "invalid image accepted"
    fi
    [[ "$("$YOLOCTL" space ssh "$SMOKE_SPACE" -- cat /proc/sys/kernel/random/boot_id)" == "$old_boot" ]] || smoke_fail "image verification failure stopped VM"
    smoke_info "VM image update: explicit replacement preserves native bytes and disk identity"
    "$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- sh -c 'printf preserved > /var/lib/yoloctl/account-state/image-update-proof'
    disk_uuid="$("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- findmnt -n -o UUID --mountpoint /var/lib/yoloctl/account-state)"
    if [[ -n "${SMOKE_UPDATE_VM_IMAGE:-}" ]]; then
        "$YOLOCTL" space recreate "$SMOKE_SPACE" --force --vm-image "$SMOKE_UPDATE_VM_IMAGE"
    else
        # Select this build's recipe, preparing it if absent. With automatic
        # initial selection this reapplies the same media; an explicit initial
        # override may differ. Failure/recovery stages use injected unit tests.
        "$YOLOCTL" space recreate "$SMOKE_SPACE" --force --update-vm-image
    fi
    [[ "$("$YOLOCTL" space ssh "$SMOKE_SPACE" -- cat /proc/sys/kernel/random/boot_id)" != "$old_boot" ]] || smoke_fail "VM image update did not recreate VM"
    [[ "$("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- cat /var/lib/yoloctl/account-state/image-update-proof)" == preserved ]] || smoke_fail "VM image update changed native bytes"
    [[ "$("$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- findmnt -n -o UUID --mountpoint /var/lib/yoloctl/account-state)" == "$disk_uuid" ]] || smoke_fail "VM image update replaced data disk"
    ! grep -q '^image_update:' "$SPACE_MANIFEST" || smoke_fail "completed update retained pending checkpoint"
fi

smoke_info "space destroy removes declarations and accounts"
"$YOLOCTL" space destroy "$SMOKE_SPACE" --force
[[ ! -e "$SPACE_MANIFEST" ]] || smoke_fail "space destroy preserved declaration"
[[ -d "$SMOKE_WORKDIR" ]] || smoke_fail "space destroy removed host workdir"

smoke_info "the destroyed name and workdir can be created again"
smoke_create_space "$SMOKE_SPACE" --workdir "$SMOKE_WORKDIR" --account default

smoke_info "PASS: space mount and destroy lifecycle"
