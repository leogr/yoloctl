#!/usr/bin/env bash
# VM disk sizes in a disposable Firecracker space: the autosized system disk,
# its grow at a recreate, and the offline data disk grow with its checks.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
[[ "$SMOKE_BACKEND" == firecracker ]] || smoke_fail "smoke-disk needs SMOKE_BACKEND=firecracker (Lima's data disk grow is refused)"
SMOKE_SPACE="${SMOKE_SPACE:-smoke-disk}"
cleanup() {
	set +e
	if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
		if [[ "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 && "${SMOKE_PASSED:-0}" != 1 ]]; then
			printf 'Retaining failed fixture at %s\n' "$SMOKE_ROOT" >&2
			return
		fi
		smoke_destroy_space
		smoke_remove_owned_root
	fi
}
trap cleanup EXIT
show() { "$YOLOCTL" space show "$SMOKE_SPACE" --json; }
field() { show | python3 -c "import json,sys; print(json.load(sys.stdin).get('$1', ''))"; }
gib=$((1 << 30))

smoke_info "disk: create autosizes the system disk from the host's free space"
smoke_create_space "$SMOKE_SPACE" --workdir "$SMOKE_WORKDIR" --account default 2> "${SMOKE_ROOT}/disk-create.log"
python3 - "${SMOKE_ROOT}/disk-create.log" "$(show)" <<'PY'
import json, re, sys
log, space = open(sys.argv[1]).read(), json.loads(sys.argv[2])
m = re.search(r'system disk (\d+) GiB \(half of (\d+) GiB free\)', log)
assert m, log
size, free = int(m[1]), int(m[2])
assert size == min(max(free // 2, 30), 50), (size, free)
assert space['disk_bytes'] == size << 30 == space['saved_disk_bytes'], space
assert 'formatting the %d GiB system disk' % size in log, log
assert space['data_disk_bytes'] == 10 << 30 and not space.get('data_disk_grow'), space
PY

smoke_info "disk: a declaration whose bytes must survive every grow"
"$YOLOCTL" box create disk-keep --space "$SMOKE_SPACE" --agent none --workdir-mode ephemeral --manifest-only --no-attach
# The declaration's own file on the data disk, hashed byte for byte (needs a
# running VM).
manifest() {
	"$YOLOCTL" space ssh "$SMOKE_SPACE" --root -- sh -c \
		'd=$(findmnt -nf -o TARGET /dev/vdb) && find "$d/config/boxes" -type f -name "disk-keep*" -exec sha256sum {} + | sort'
}
manifest > "${SMOKE_ROOT}/disk-keep.json"
[[ -s "${SMOKE_ROOT}/disk-keep.json" ]] || smoke_fail "no declaration file for disk-keep on the data disk"

smoke_info "disk: the system disk grows at the next recreate only"
size=$(( $(field disk_bytes) / gib ))
"$YOLOCTL" space stop "$SMOKE_SPACE"
"$YOLOCTL" space set "$SMOKE_SPACE" --disk "$((size - 1))GiB" && smoke_fail "the system disk shrank"
"$YOLOCTL" space set "$SMOKE_SPACE" --disk "$((size + 5))GiB" | tee "${SMOKE_ROOT}/disk-set.log"
grep -q "applies at the next yoloctl space recreate" "${SMOKE_ROOT}/disk-set.log"
"$YOLOCTL" space start "$SMOKE_SPACE"
[[ "$(field disk_bytes)" == "$((size * gib))" ]] || smoke_fail "start resized the system disk"
"$YOLOCTL" space recreate "$SMOKE_SPACE" --force | tee "${SMOKE_ROOT}/disk-recreate.log"
grep -q "system disk ${size} → $((size + 5)) GiB" "${SMOKE_ROOT}/disk-recreate.log"
[[ "$(field disk_bytes)" == "$(((size + 5) * gib))" ]] || smoke_fail "recreate did not apply the saved disk"

smoke_info "disk: the data disk grows offline and keeps its data"
"$YOLOCTL" space stop "$SMOKE_SPACE"
"$YOLOCTL" space set "$SMOKE_SPACE" --data-disk 8GiB && smoke_fail "the data disk shrank"
backup=--
if ! "$YOLOCTL" space set "$SMOKE_SPACE" --data-disk 20GiB 2> "${SMOKE_ROOT}/disk-grow.err"; then
	grep -q -- "--no-backup" "${SMOKE_ROOT}/disk-grow.err" || { cat "${SMOKE_ROOT}/disk-grow.err" >&2; smoke_fail "data disk grow refused"; }
	smoke_info "disk: this host cannot reflink; growing without the copy"
	backup=--no-backup
	"$YOLOCTL" space set "$SMOKE_SPACE" --data-disk 20GiB --no-backup
fi
[[ "$(field data_disk_grow)" == extended ]] || smoke_fail "no extended checkpoint"
"$YOLOCTL" space export "$SMOKE_SPACE" "${SMOKE_ROOT}/disk.tar.gz" && smoke_fail "exported with a grow pending"
"$YOLOCTL" space start "$SMOKE_SPACE"
"$YOLOCTL" space stop "$SMOKE_SPACE" | tee "${SMOKE_ROOT}/disk-stop.log"
grep -q "data disk 10 GiB → 20 GiB, verified" "${SMOKE_ROOT}/disk-stop.log"
[[ -z "$(field data_disk_grow)" && "$(field data_disk_bytes)" == "$((20 * gib))" ]] || smoke_fail "grow not recorded"
disk="${SMOKE_CONFIG_ROOT}/firecracker/disks/$(field account_disk_id)"
[[ ! -e "${disk}/data.ext4.pre-grow" ]] || smoke_fail "the pre-grow copy was kept after verification"
if command -v e2fsck >/dev/null 2>&1; then
	e2fsck -f -n "${disk}/data.ext4"
fi
"$YOLOCTL" space start "$SMOKE_SPACE"
manifest | cmp - "${SMOKE_ROOT}/disk-keep.json" || smoke_fail "the declaration changed"
"$YOLOCTL" space ssh "$SMOKE_SPACE" -- findmnt -nf -b -o SIZE /dev/vdb |
	python3 -c "import sys; n=int(sys.stdin.read()); assert 19 << 30 < n <= 20 << 30, n"
smoke_info "disk: all checks passed (data disk grow ${backup})"
SMOKE_PASSED=1
