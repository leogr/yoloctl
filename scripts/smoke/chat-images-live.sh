#!/usr/bin/env bash
# Human-guided live check of chat image previews on a real host terminal.
# It builds a new isolated space with a managed chat room, has an agentless
# participant publish fixture PNGs through the real AIRC artifact path, then
# walks the operator through finite rows in the TUI and records the answers.
# It always creates its own fixture root and never adopts an existing space.
#
# Exit status: 0 complete (every required row PASS, UIs exited normally,
# cleanup succeeded or was explicitly preserved), 1 failed (a FAIL row, an
# abnormal UI exit, a setup error or failed cleanup), 3 incomplete (a required
# row skipped or never answered, or a terminal that cannot qualify images).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

[[ "${YOLOCTL_TUI:-}" != 1 ]] || smoke_fail "run this from the host terminal itself, not inside a yoloctl tab"
# Actually open the controlling terminal; permission bits alone prove nothing.
(: </dev/tty >/dev/tty) 2>/dev/null || smoke_fail "cannot open the controlling terminal /dev/tty"
# Record the terminal before smoke_init replaces stdin.
TERM_NAME="${TERM_PROGRAM:-${TERM:-unknown}}"
TERM_VERSION="${TERM_PROGRAM_VERSION:-unknown}"
HOST_COLORTERM="${COLORTERM:-}"
IMAGE_TERMINAL=0
case "$TERM_NAME" in
ghostty | xterm-kitty | kitty) IMAGE_TERMINAL=1 ;;
*) smoke_info "WARNING: terminal '$TERM_NAME' is not Ghostty or Kitty; this run cannot complete an image qualification" ;;
esac

# A new owned fixture root every time: inherited smoke settings never select
# an existing root, home or space.
unset SMOKE_ROOT SMOKE_SPACE
SMOKE_SPACE=chat-images-live
smoke_init
[[ "$SMOKE_OWNS_ROOT" == 1 ]] || smoke_fail "fixture root is not owned by this run"
[[ ! -e "$SMOKE_CONFIG_ROOT/spaces/$SMOKE_SPACE" ]] || smoke_fail "space $SMOKE_SPACE already exists in the new fixture root"
REPORT="${CHAT_IMAGES_REPORT:-$(dirname "$SMOKE_ROOT")/chat-images-live-$(date -u +%Y%m%dT%H%M%SZ).md}"
[[ ! -e "$REPORT" ]] || smoke_fail "report $REPORT already exists"
POSTER=poster
REQUIRED="1a 1b 1c 1d 1e 1f 1g 1h 2 3a 3b 3c"
PHASE=setup
SPACE_ATTEMPTED=0
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
version="$("$YOLOCTL" version --json | python3 -c 'import json,sys; v=json.load(sys.stdin); print(v["yoloctl"], v["bundle"])')"
if commit="$(git -C "$repo" rev-parse HEAD 2>/dev/null)"; then
    git -C "$repo" diff --quiet HEAD -- || commit="$commit (dirty)"
else
    commit=unknown
fi
version="$version, checkout $commit"
{
    printf '# Chat image previews: live check\n\n'
    printf -- '- Date (UTC): %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf -- '- Host: %s %s %s\n' "$(uname -s)" "$(sw_vers -productVersion 2>/dev/null || uname -r)" "$(uname -m)"
    printf -- '- Terminal: %s %s, COLORTERM=%s\n' "$TERM_NAME" "$TERM_VERSION" "${HOST_COLORTERM:-unset}"
    printf -- '- yoloctl: %s, backend %s\n' "$version" "$SMOKE_BACKEND"
    printf -- '- Fixture: root %s, space %s, managed room, agentless participant %s\n' "$SMOKE_ROOT" "$SMOKE_SPACE" "$POSTER"
    printf -- '- Required rows: %s; row 4 is optional\n\n' "$REQUIRED"
    printf '| Row | Check | Result | Note |\n|---|---|---|---|\n'
} >"$REPORT"

row() { # row ID CHECK RESULT NOTE
    printf '| %s | %s | %s | %s |\n' "$1" "$2" "$3" "${4//|//}" >>"$REPORT"
}

# remove_root removes the owned fixture root and says whether it is gone.
remove_root() {
    if smoke_remove_owned_root && [[ ! -e "$SMOKE_ROOT" ]]; then
        echo "fixture root removed"
    else
        echo "FAILED: fixture root $SMOKE_ROOT was not removed"
    fi
}

# finish runs on every exit, including errors and interrupts: it records
# unanswered required rows, the cleanup outcome and the overall disposition.
finish() {
    local status=$? id result failed=0 incomplete=0 cleanup
    set +e
    trap - EXIT INT TERM HUP
    case "$status" in
    0) ;;
    129 | 130 | 143) incomplete=1; row "run" "interrupted during $PHASE" INTERRUPTED "exit $status" ;;
    *) failed=1; row "run" "script ended during $PHASE" FAIL "exit $status" ;;
    esac
    for id in $REQUIRED; do
        result="$(grep -E "^\| $id \|" "$REPORT" | awk -F' [|] ' '{print $3}' | tail -1)"
        case "$result" in
        PASS) ;;
        FAIL) failed=1 ;;
        SKIP) incomplete=1 ;;
        *) incomplete=1; row "$id" "not answered" "NOT RUN" "" ;;
        esac
    done
    grep -q '| FAIL |' "$REPORT" && failed=1
    [[ "$IMAGE_TERMINAL" == 1 ]] || incomplete=1
    if [[ "${SMOKE_PRESERVE_SPACE:-0}" == 1 || ( ( "$failed" == 1 || "$status" != 0 ) && "${SMOKE_KEEP_ON_FAILURE:-0}" == 1 ) ]]; then
        cleanup="preserved by request: root $SMOKE_ROOT (YOLOCTL_HOME=$YOLOCTL_HOME)"
    elif [[ "$SPACE_ATTEMPTED" == 0 ]]; then
        cleanup="no space was created; $(remove_root)"
    elif smoke_destroy_space; then
        cleanup="space destroy --force acknowledged success; $(remove_root)"
    else
        cleanup="FAILED: space destroy did not succeed; recovery state retained at $SMOKE_ROOT"
        failed=1
    fi
    [[ "$cleanup" != *FAILED* ]] || failed=1
    local disposition=COMPLETE code=0
    if [[ "$failed" == 1 ]]; then
        disposition=FAILED code=1
    elif [[ "$incomplete" == 1 ]]; then
        disposition=INCOMPLETE code=3
    fi
    [[ "$IMAGE_TERMINAL" == 1 ]] || disposition="$disposition (terminal $TERM_NAME is not Ghostty or Kitty: not an image qualification)"
    printf '\n- Cleanup: %s\n- Disposition: %s (exit %s)\n' "$cleanup" "$disposition" "$code" >>"$REPORT"
    smoke_info "$disposition; report: $REPORT"
    exit "$code"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

# Fixture PNGs, written without third-party modules: an alpha gradient, a wide
# image, a non-PNG with a .png name, and a valid PNG over the decode bound.
FIXTURES="$SMOKE_ROOT/fixtures"
mkdir -p "$FIXTURES"
python3 -I - "$FIXTURES" <<'PY'
import struct, sys, zlib
out = sys.argv[1]
def png(path, w, h, pixel):
    rows = b"".join(b"\0" + b"".join(pixel(x, y) for x in range(w)) for y in range(h))
    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))
    with open(path, "wb") as f:
        f.write(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 6, 0, 0, 0))
                + chunk(b"IDAT", zlib.compress(rows, 9)) + chunk(b"IEND", b""))
png(f"{out}/gradient.png", 160, 120, lambda x, y: bytes((x * 255 // 159, y * 255 // 119, 128, 64 + 191 * x // 159)))
png(f"{out}/wide.png", 400, 50, lambda x, y: bytes((255, 255 * ((x // 25) % 2), 0, 255)))
with open(f"{out}/not-an-image.png", "wb") as f:
    f.write(b"this is plain text with a .png name\n")
# 5000x4000 = 20 Mi pixels, over the 16 Mi decode bound; compresses to a few KiB.
w, h = 5000, 4000
row = b"\0" + b"\0\0\0\xff" * w
with open(f"{out}/too-many-pixels.png", "wb") as f:
    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))
    f.write(b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 6, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(row * h, 9)) + chunk(b"IEND", b""))
PY

smoke_info "creating isolated space $SMOKE_SPACE in $SMOKE_ROOT"
SPACE_ATTEMPTED=1
smoke_create_space "$SMOKE_SPACE" --workdir "$SMOKE_WORKDIR" --account default
"$YOLOCTL" chat ensure --space "$SMOKE_SPACE" --box chat --human qa
"$YOLOCTL" box create "$POSTER" --space "$SMOKE_SPACE" --agent none --account none \
    --workdir-mode ephemeral --features yolo.chat.skill --no-attach --no-autoupdate
"$YOLOCTL" chat enroll "$POSTER" --space "$SMOKE_SPACE" --connect --client space --recreate
in_poster() { "$YOLOCTL" box exec "$POSTER" --space "$SMOKE_SPACE" -- "$@"; }
for name in gradient.png wide.png not-an-image.png too-many-pixels.png; do
    in_poster sh -c 'airc artifact upload --config "$YOLOCTL_CHAT_CONFIG" --channel "#space" --name "$1" --stdin' \
        upload "$name" <"$FIXTURES/$name"
    smoke_info "published $name"
done
PHASE=rows

ask() { # ask ID QUESTION: records PASS/FAIL/SKIP and a note
    local id="$1" question="$2" answer note
    while :; do
        printf '\n[%s] %s\n  p = pass, f = fail, s = skip: ' "$id" "$question" >/dev/tty
        read -r answer </dev/tty || exit 130 # input closed: an interruption
        case "$answer" in p) answer=PASS ;; f) answer=FAIL ;; s) answer=SKIP ;; *) continue ;; esac
        break
    done
    printf '  note (optional): ' >/dev/tty
    read -r note </dev/tty || exit 130
    row "$id" "$question" "$answer" "$note"
}
step() { printf '\n==== %s\n%s\n' "$1" "$2" >/dev/tty; }
# run_ui ID LABEL COMMAND...: runs a UI on this terminal until the operator
# quits it and records its exit status as its own row; a nonzero status is a
# FAIL that later answers cannot erase.
run_ui() {
    local id="$1" label="$2" status=0
    shift 2
    printf '\nPress Enter to start %s; quit it with Ctrl+C twice when done. ' "$label" >/dev/tty
    read -r _ </dev/tty || exit 130
    "$@" </dev/tty >/dev/tty 2>/dev/tty || status=$?
    if [[ "$status" == 0 ]]; then
        row "$id" "$label exited normally" PASS "exit 0"
    else
        row "$id" "$label exited abnormally" FAIL "exit $status"
        printf '\n%s exited with status %s; this run is FAILED. Continue with the remaining rows anyway.\n' "$label" "$status" >/dev/tty
    fi
}

OPEN_CHAT="In the TUI palette type:  chat --space $SMOKE_SPACE   (Enter). In the chat tab
select a message with Alt+Up/Down; its actions include Show image when previews apply."
step "1. Draw, aspect, errors, copy, cache" "$OPEN_CHAT
Then, in this order:
  a) gradient.png → Show image: the gradient appears under the message, alpha
     blending over the pane background.
  b) wide.png → Show image: gradient.png collapses to its chip; wide.png keeps
     its 8:1 aspect with crisp stripes.
  c) Drag-select across the wide image rows and copy; paste anywhere: the
     text is the chip, e.g. [file wide.png · …], never placeholder glyphs.
  d) Hide image, then Show image again: it reappears at once (visual check
     only; the download count is not measured).
  e) not-an-image.png → Show image: notice 'the file is not a PNG image'.
  f) too-many-pixels.png → Show image: notice 'the image is too large'.
  g) Resize the window, then switch to another tab and back: the image is
     redrawn in place or collapsed to its chip; no image is left behind.
  h) Quit the TUI: no image remains on the terminal."
run_ui ui-1 "the TUI" "$YOLOCTL"
ask 1a "gradient.png draws under its message with alpha"
ask 1b "showing wide.png collapses gradient.png; aspect and stripes correct"
ask 1c "copy over preview rows pastes the chip text"
ask 1d "hide then show re-displays at once"
ask 1e "non-PNG reports 'not a PNG' and keeps the chip"
ask 1f "over-bound PNG reports 'too large' and keeps the chip"
ask 1g "resize and tab switch leave no stray image"
ask 1h "quitting the TUI leaves no image on the terminal"

step "2. Child colour profile without COLORTERM" "Same steps with COLORTERM removed from the environment.
$OPEN_CHAT
Expected: no message offers Show image; chips stay as before."
run_ui ui-2 "the TUI without COLORTERM" env -u COLORTERM "$YOLOCTL"
ask 2 "without COLORTERM chat offers no Show image and keeps chips"

step "3. Standalone chat" "This opens the chat view directly, outside the TUI, where it is its own image host.
  a) gradient.png → Show image: the gradient appears under the message.
  b) wide.png → Show image: gradient.png collapses to its chip; wide.png appears.
  c) Quit: no image remains on the terminal."
run_ui ui-3 "standalone chat" "$YOLOCTL" chat --space "$SMOKE_SPACE"
ask 3a "standalone chat draws gradient.png"
ask 3b "standalone chat replaces it with wide.png"
ask 3c "quitting standalone chat leaves no image on the terminal"

step "4. Optional: another terminal" "In a terminal without Kitty graphics (for example Terminal.app) run:
  YOLOCTL_HOME='$YOLOCTL_HOME' XDG_CONFIG_HOME='$XDG_CONFIG_HOME' XDG_CACHE_HOME='$XDG_CACHE_HOME' '$YOLOCTL'
then open the chat as above. Expected: no Show image; chips stay. Answer here when done (s to skip)."
ask 4 "a terminal without Kitty graphics offers no Show image"

PHASE=done
