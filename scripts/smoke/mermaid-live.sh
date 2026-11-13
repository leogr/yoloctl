#!/usr/bin/env bash
# Human-guided live check of Mermaid drawings on a real host terminal.
# It builds a new isolated space with a managed chat room, has an agentless
# participant post diagram messages through the real AIRC path, then walks the
# operator through finite rows in the TUI chat, standalone chat and follow, and
# records the answers. It always creates its own fixture root and never adopts
# an existing space.
#
# Exit status: 0 complete (every required row PASS, UIs exited normally,
# cleanup succeeded or was explicitly preserved), 1 failed (a FAIL row, an
# abnormal UI exit, a setup error or failed cleanup), 3 incomplete (a required
# row skipped or never answered).
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"

[[ "${YOLOCTL_TUI:-}" != 1 ]] || smoke_fail "run this from the host terminal itself, not inside a yoloctl tab"
(: </dev/tty >/dev/tty) 2>/dev/null || smoke_fail "cannot open the controlling terminal /dev/tty"
TERM_NAME="${TERM_PROGRAM:-${TERM:-unknown}}"
TERM_VERSION="${TERM_PROGRAM_VERSION:-unknown}"
case "$TERM_NAME" in
ghostty | xterm-kitty | kitty | WezTerm) ;;
*) smoke_info "NOTE: terminal '$TERM_NAME' may lack the Symbols for Legacy Computing glyphs used by decision diamonds" ;;
esac

unset SMOKE_ROOT SMOKE_SPACE
SMOKE_SPACE=mermaid-live
smoke_init
[[ "$SMOKE_OWNS_ROOT" == 1 ]] || smoke_fail "fixture root is not owned by this run"
[[ ! -e "$SMOKE_CONFIG_ROOT/spaces/$SMOKE_SPACE" ]] || smoke_fail "space $SMOKE_SPACE already exists in the new fixture root"
REPORT="${MERMAID_LIVE_REPORT:-$(dirname "$SMOKE_ROOT")/mermaid-live-$(date -u +%Y%m%dT%H%M%SZ).md}"
[[ ! -e "$REPORT" ]] || smoke_fail "report $REPORT already exists"
POSTER=poster
REQUIRED="1a 1b 1c 1d 1e 1f 1g 1h 1i 1j 2 3a 3b 4"
PHASE=setup
SPACE_ATTEMPTED=0
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
version="$("$YOLOCTL" version --json | python3 -c 'import json,sys; v=json.load(sys.stdin); print(v["yoloctl"], v["bundle"])')"
if commit="$(git -C "$repo" rev-parse HEAD 2>/dev/null)"; then
    git -C "$repo" diff --quiet HEAD -- || commit="$commit (dirty)"
else
    commit=unknown
fi
{
    printf '# Mermaid drawings: live check\n\n'
    printf -- '- Date (UTC): %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf -- '- Host: %s %s %s\n' "$(uname -s)" "$(sw_vers -productVersion 2>/dev/null || uname -r)" "$(uname -m)"
    printf -- '- Terminal: %s %s\n' "$TERM_NAME" "$TERM_VERSION"
    printf -- '- yoloctl: %s, checkout %s, backend %s\n' "$version" "$commit" "$SMOKE_BACKEND"
    printf -- '- Fixture: root %s, space %s, managed room, agentless participant %s\n' "$SMOKE_ROOT" "$SMOKE_SPACE" "$POSTER"
    printf -- '- Required rows: %s\n\n' "$REQUIRED"
    printf '| Row | Check | Result | Note |\n|---|---|---|---|\n'
} >"$REPORT"

row() { # row ID CHECK RESULT NOTE
    printf '| %s | %s | %s | %s |\n' "$1" "$2" "$3" "${4//|//}" >>"$REPORT"
}

remove_root() {
    if smoke_remove_owned_root && [[ ! -e "$SMOKE_ROOT" ]]; then
        echo "fixture root removed"
    else
        echo "FAILED: fixture root $SMOKE_ROOT was not removed"
    fi
}

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
    printf '\n- Cleanup: %s\n- Disposition: %s (exit %s)\n' "$cleanup" "$disposition" "$code" >>"$REPORT"
    smoke_info "$disposition; report: $REPORT"
    exit "$code"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

# The diagram messages: one per fix leogr reviewed, plus the two fallbacks.
FIXTURES="$SMOKE_ROOT/fixtures"
mkdir -p "$FIXTURES"
python3 -I - "$FIXTURES" <<'PY'
import sys
out = sys.argv[1]
fence = "`" * 3
messages = [
    ("1-decision", "Decision node:", "flowchart TD\n    T[Ticket] --> R{Review}\n    R -->|changes| B[Build]\n    B --> R\n    R -->|accepted| M[Merge]"),
    ("2-subgraphs", "Subgraphs:", "flowchart LR\n    subgraph Host\n        CLI[yoloctl CLI] --> TUI[TUI]\n    end\n    subgraph VM\n        CTL[controller] --> BOX[box coder]\n    end\n    CLI --> CTL\n    TUI --> CTL"),
    ("3-fanout", "Fan-out:", "graph TD\n    coord[Coordinator] --> a[astra] & s[sol-dev] & o[opus-dev] & d[devOpSol]"),
    ("4-sequence", "Sequence:", "sequenceDiagram\n    participant U as User\n    participant C as Controller\n    participant B as Box\n    U->>C: box start coder\n    C->>B: launch holder\n    alt resume\n        B-->>C: SessionStart resume\n    else new\n        B-->>C: SessionStart startup\n    end\n    C-->>U: attached\n    Note right of C: holder answers terminal queries once"),
    ("5-unsupported", "Unsupported type (falls back to source):", "classDiagram\n    Animal <|-- Duck"),
    ("6-misplaced-note", "Misplaced note (falls back to source):", "sequenceDiagram\n    A->>B: request\n    alt ok\n        B-->>A: done\n    end\n    Note right of B: after the block"),
]
with open(f"{out}/diagrams.md", "w") as doc:
    doc.write("# Mermaid live check\n")
    for name, caption, body in messages:
        text = f"{caption}\n{fence}mermaid\n{body}\n{fence}"
        with open(f"{out}/{name}.txt", "w") as f:
            f.write(text)
        doc.write(f"\n## {caption}\n\n{fence}mermaid\n{body}\n{fence}\n")
PY

smoke_info "creating isolated space $SMOKE_SPACE in $SMOKE_ROOT"
SPACE_ATTEMPTED=1
smoke_create_space "$SMOKE_SPACE" --workdir "$SMOKE_WORKDIR" --account default
"$YOLOCTL" chat ensure --space "$SMOKE_SPACE" --box chat --human qa
"$YOLOCTL" box create "$POSTER" --space "$SMOKE_SPACE" --agent none --account none \
    --workdir-mode ephemeral --features yolo.chat.skill --no-attach --no-autoupdate
"$YOLOCTL" chat enroll "$POSTER" --space "$SMOKE_SPACE" --connect --client space --recreate
in_poster() { "$YOLOCTL" box exec "$POSTER" --space "$SMOKE_SPACE" -- "$@"; }
for message in "$FIXTURES"/[1-6]-*.txt; do
    in_poster sh -c 'airc send space "#space" --stdin' <"$message"
    smoke_info "posted $(basename "$message" .txt)"
done
PHASE=rows

ask() { # ask ID QUESTION: records PASS/FAIL/SKIP and a note
    local id="$1" question="$2" answer note
    while :; do
        printf '\n[%s] %s\n  p = pass, f = fail, s = skip: ' "$id" "$question" >/dev/tty
        read -r answer </dev/tty || exit 130
        case "$answer" in p) answer=PASS ;; f) answer=FAIL ;; s) answer=SKIP ;; *) continue ;; esac
        break
    done
    printf '  note (optional): ' >/dev/tty
    read -r note </dev/tty || exit 130
    row "$id" "$question" "$answer" "$note"
}
step() { printf '\n==== %s\n%s\n' "$1" "$2" >/dev/tty; }
run_ui() { # run_ui ID LABEL COMMAND...
    local id="$1" label="$2" status=0
    shift 2
    printf '\nPress Enter to start %s; quit it when done. ' "$label" >/dev/tty
    read -r _ </dev/tty || exit 130
    "$@" </dev/tty >/dev/tty 2>/dev/tty || status=$?
    if [[ "$status" == 0 ]]; then
        row "$id" "$label exited normally" PASS "exit 0"
    else
        row "$id" "$label exited abnormally" FAIL "exit $status"
        printf '\n%s exited with status %s; this run is FAILED. Continue with the remaining rows anyway.\n' "$label" "$status" >/dev/tty
    fi
}

step "1. Chat in the TUI" "In the TUI palette type:  chat --space $SMOKE_SPACE   (Enter). Use a wide window
(about 120 columns), then check, in this order:
  a) Decision node: a shallow diamond with flat ends (no box, no missing-glyph boxes).
  b) Subgraphs: edges cross the Host/VM borders at single cells; none runs along a border.
  c) Fan-out: one trunk under Coordinator, a bar and a drop into each child.
  d) Sequence: whole names, no grey bands in alt, solid lifelines joined to the arrows.
  e) Unsupported type: the code card stays, with 'mermaid: not drawn (…)' under it.
  f) Misplaced note: the code card stays, with its reason under it.
  g) Narrow the window to about 50 columns: wide diagrams switch to their code with
     'needs N columns'; widen again: the drawings come back.
  h) Drag-select over a drawing: the pasted text is the drawing. The card's copy
     control copies the mermaid source.
  i) Highlight a whole drawing, Alt+Q: the draft quotes the mermaid fence source
     (Ctrl+Z removes it).
  j) Ctrl+S shows the source fences; Ctrl+S again shows the drawings."
run_ui ui-1 "the TUI" "$YOLOCTL"
ask 1a "decision node is a shallow diamond with flat ends"
ask 1b "subgraph edges cross borders, none runs along one"
ask 1c "fan-out is one trunk, a bar and drops"
ask 1d "sequence: whole names, no grey bands, joined lifelines"
ask 1e "unsupported type keeps its code with a reason"
ask 1f "misplaced note keeps its code with a reason"
ask 1g "narrow pane falls back to code with 'needs N columns' and recovers"
ask 1h "drag copy gives the drawing; copy control gives the source"
ask 1i "Alt+Q on a whole drawing quotes the fence source"
ask 1j "Ctrl+S toggles between source and drawings"

step "2. Standalone chat" "This opens the chat view directly, outside the TUI. Check that the same six
messages show as in step 1 (drawings and the two fallbacks), then quit."
run_ui ui-2 "standalone chat" "$YOLOCTL" chat --space "$SMOKE_SPACE"
ask 2 "standalone chat draws the same diagrams and fallbacks"

step "3. Follow" "This follows a Markdown file with the same six diagrams.
  a) The diagrams draw as in chat, with the two fallbacks.
  b) Resize the window: the drawings are redrawn for the new width (or fall back to
     code with 'needs N columns' when too narrow). Quit with q."
run_ui ui-3 "follow" "$YOLOCTL" follow --host "$FIXTURES/diagrams.md"
ask 3a "follow draws the same diagrams and fallbacks"
ask 3b "follow redraws or falls back on resize"

printf 'markdown:\n  mermaid: false\n' >"$SMOKE_CONFIG_ROOT/tui.yaml"
step "4. Preference off" "markdown.mermaid is now false in this fixture's tui.yaml. Open the chat view:
every diagram stays a code card, with no 'mermaid:' note. Quit."
run_ui ui-4 "standalone chat with markdown.mermaid off" "$YOLOCTL" chat --space "$SMOKE_SPACE"
ask 4 "with markdown.mermaid off every diagram stays a code card"

PHASE=done
