#!/usr/bin/env bash
# Isolated image/session integration; never targets an existing user space.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
smoke_init
BOX=smoke-airc
cleanup() {
    local exit_status=$?
    set +e
    if [[ "${SMOKE_PRESERVE_SPACE:-0}" == 1 || ( "$exit_status" != 0 && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ) ]]; then
        smoke_info "retaining isolated space and logs at $SMOKE_ROOT"
    else
        smoke_destroy_space
        smoke_remove_owned_root
    fi
}
trap cleanup EXIT
if [[ -f "$SMOKE_CONFIG_ROOT/spaces/$SMOKE_SPACE/space.yaml" ]]; then
    # make rebuilds all companions with a new bundle identity. A retained
    # disposable fixture must explicitly select that build before box commands.
    smoke_info "updating retained isolated space $SMOKE_SPACE"
    "$YOLOCTL" space start "$SMOKE_SPACE" --update-controller
else
    smoke_ensure_space
fi
for fixture in smoke-airc smoke-airc-session smoke-airc-room smoke-airc-a smoke-airc-b; do
    smoke_destroy_box "$fixture"
done
# The PTY grant is for the nested test driver. The managed session itself uses
# the holder's already allocated PTY and does not need this capability. Like the
# managed room's server, the session runs aircd directly.
"$YOLOCTL" box create "$BOX" --space "$SMOKE_SPACE" --agent none --account none \
    --workdir-mode ephemeral --features yolo.chat.skill --capability fs.pty.allocate \
    --session-command /bin/sh --session-command=-c \
    --session-command 'umask 077; exec aircd serve --config .airc/server.toml' \
    --no-attach --no-autoupdate
smoke_check_image_identity "$BOX"
in_box() { "$YOLOCTL" box exec "$BOX" --space "$SMOKE_SPACE" -- "$@"; }
in_box airc --version
in_box aircd --version
# The image must carry the pinned source revision, not merely some AIRC build.
pinned="$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["revision"])' \
    "$(dirname "${BASH_SOURCE[0]}")/../../internal/features/airc-source/manifest.json")"
installed="$(in_box cat /usr/local/share/airc/revision)"
[[ "$pinned" =~ ^[0-9a-f]{40}$ && "$installed" == "$pinned" ]] ||
    smoke_fail "installed AIRC revision '$installed' does not match the pinned '$pinned'"
smoke_info "AIRC revision $installed matches the pin"
in_box sh -c '! command -v repartee && ! command -v airc-box'
in_box test -f /etc/codex/skills/airc/SKILL.md
in_box test -f /etc/claude-code/.claude/skills/airc/SKILL.md
in_box test -f /home/sandbox/.agents/skills/airc/SKILL.md
# The pinned AIRC ships the opt-in Codex wait-for-idle route beside the default
# one; nothing here selects it.
in_box sh -c 'airc-codex --help | grep -q -- --wait-for-idle && airc-codex --help | grep -q -- --idle-timeout'
in_box sh -c 'airc subscribe --help | grep -q "0 sets no deadline"'
in_box grep -q '^## Optional wait for idle' /etc/codex/skills/airc/references/codex.md
# Packaging exposes the new route and its recovery recipe without activating it
# or addressing any existing Codex conversation.
in_box sh -c 'airc-codex --help | grep -q -- --app-server-socket'
in_box grep -q '^## Optional active-turn delivery' /etc/codex/skills/airc/references/codex.md
in_box sh -c 'cat > /tmp/airc-runtime.py' <"$(dirname "${BASH_SOURCE[0]}")/airc-runtime.py"
in_box python3 /tmp/airc-runtime.py
# The production managed session must work without the nested driver's PTY grant.
"$YOLOCTL" box copy "$BOX" smoke-airc-session --space "$SMOKE_SPACE" \
    --drop-capability fs.pty.allocate --no-attach --no-autoupdate
smoke_destroy_box "$BOX"
BOX=smoke-airc-session
in_box aircd init .airc
python3 "$(dirname "${BASH_SOURCE[0]}")/airc-attach.py" \
    --yoloctl "$YOLOCTL" --box "$BOX" --space "$SMOKE_SPACE" --output "$SMOKE_ROOT/airc-attach"
"$YOLOCTL" box restart "$BOX" --space "$SMOKE_SPACE" --no-attach --no-autoupdate
python3 "$(dirname "${BASH_SOURCE[0]}")/airc-attach.py" \
    --yoloctl "$YOLOCTL" --box "$BOX" --space "$SMOKE_SPACE" --output "$SMOKE_ROOT/airc-revive"
python3 "$(dirname "${BASH_SOURCE[0]}")/airc-cross-box.py" \
    --yoloctl "$YOLOCTL" --space "$SMOKE_SPACE" --output "$SMOKE_ROOT/airc-cross-box"
smoke_info 'PASS: AIRC image, confined collaboration, managed sessions and cross-box history/artifacts'
