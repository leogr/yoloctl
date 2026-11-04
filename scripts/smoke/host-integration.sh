#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
if [[ "$(uname -s)" != Linux ]]; then
    smoke_info "SKIP: host-integration uses Linux desktop stand-ins; native macOS acceptance is separate"
    exit 0
fi
smoke_init
BOX="smoke-host-integration"
cleanup() {
    local status=$?
    set +e
    if [[ "$status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ]]; then
        printf 'Smoke failed; isolated VM and captures retained at %s\n' "$SMOKE_ROOT" >&2
        return
    fi
    if [[ "${SMOKE_PRESERVE_SPACE:-0}" != 1 ]]; then
        smoke_destroy_box "$BOX"
        smoke_destroy_space
        smoke_remove_owned_root
    fi
}
trap cleanup EXIT
smoke_ensure_space
smoke_destroy_box "$BOX"
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --workdir "$SMOKE_WORKDIR" --no-attach --no-autoupdate
python3 "$(dirname "${BASH_SOURCE[0]}")/host-integration.py" \
    --yoloctl "$YOLOCTL" --space "$SMOKE_SPACE" --box "$BOX" \
    --output "$SMOKE_ROOT/host-integration" --workdir "$SMOKE_WORKDIR"
