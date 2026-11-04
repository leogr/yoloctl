# Native selection and explicit copy

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Plain left drag selects local text in tab content when the child does
not request mouse reporting, when viewing history, or after the child exits.
Mouse-reporting applications retain their input. The palette has no local
selection handler; the host terminal owns its selection/copy interaction.
Local selection gestures retain their originating surface. [1](#source-1)
[2](#source-2) [6](#source-6)

[FACT] The pinned core's `SelectionGesture` and tracked native screen pins
implement character, double-click word, triple-click line and Alt-rectangle
selection. The adapter uses cell centres and a 500 ms repeat interval, not
host pixel geometry or desktop click settings. Dragging above/below the
selection's pane (the whole tab when undivided) scrolls one row every 40 ms
while held; release stops this timer. In a divided tab the selection stays
inside its pane [19](#source-19). Selection
text comes from native cells, unwraps soft wraps and preserves hard line breaks.
Its bounded formatter rejects oversized output rather than truncating it.
[1](#source-1) [3](#source-3) [4](#source-4)

[FACT] Frame snapshots include selection spans alongside text/cursor data.
Highlighting reverses selected grapheme styles without extracting clipboard
text from ANSI strings. Pins follow output/reflow; screen changes invalidate
an active gesture. During synchronized-output holds, new selection events are
ignored and copying a visible existing selection gives a retry notice rather
than copying hidden live text. Typing/paste clears local selection; modifier-only
presses preserve it for the following copy chord. [1](#source-1) [4](#source-4)
[5](#source-5) [7](#source-7) [8](#source-8)

[FACT] Ctrl+Shift+C, or Command+C on a macOS host build, copies an existing
local tab selection if the host forwards the key. With no local selection,
the key follows ordinary application routing. Yoloctl local selection never writes the
clipboard on mouse release; applications retain their own selection policy. Copy uses the same validated, size-limited outer OSC 52 policy as
application writes; disabling that policy also blocks local copy. A host-owned
selection and copy binding are handled before yoloctl receives input.
[1](#source-1) [6](#source-6) [9](#source-9)

[FACT, upstream configuration] The pinned Ghostty source sets its standard
copy keybinding to `performable`: it consumes the key only when its action is
available. This permits forwarding when Ghostty has no selection. Other host
versions/configurations can consume the chord; the automated tests do not
establish that every host delivers it. [10](#source-10) [11](#source-11)

[FACT] Tests cover Unicode text, soft/hard wraps, backward/rectangular selection,
word/line selection, reflow/history/reset, eviction and bounded extraction.
Real PTY tests assert application input, highlight columns, explicit copy,
clipboard policy, palette exclusion, modifier preservation and stopping
autoscroll. These are bounded contracts; native Ghostty/Kitty selection and
clipboard behavior still require graphical acceptance. [6](#source-6)
[7](#source-7) [8](#source-8)

[FACT, version-specific upstream] Codex 0.162.1 `copy_on_select = auto`
disables automatic copy for Ghostty >= 1.2 when its terminal name/version is
recognized; unknown terminals enable it. Yoloctl forwards bounded printable
`TERM_PROGRAM` and `TERM_PROGRAM_VERSION` from the host request to the confined
session command. It preserves `TERM` and query ownership. Existing processes
retain their launch environment; reattachment does not update it. Explicit
agent preferences still win. [12](#source-12) [13](#source-13) [14](#source-14)
[15](#source-15) [16](#source-16) [17](#source-17) [18](#source-18)

## Sources

1. <a id="source-1"></a> [selection.go](../../internal/tui/selection.go#L1-L144) — selection eligibility, autoscroll, key copy and highlighting.
2. <a id="source-2"></a> [mouse.go](../../internal/tui/mouse.go#L1-L273) — gesture ownership, local selection and application routing.
3. <a id="source-3"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L411-L460) — native gesture bridge and bounded plain-text formatter.
4. <a id="source-4"></a> [selection.go](../../internal/terminal/selection.go#L1-L71) — viewport mapping, validation, clearing and text extraction.
5. <a id="source-5"></a> [selection.go](../../internal/tui/term/selection.go#L1-L50) — terminal locking and synchronized-output handling.
6. <a id="source-6"></a> [selection_test.go](../../internal/tui/selection_test.go#L1-L128) — real PTY selection/copy, policy, palette/application boundaries and autoscroll.
7. <a id="source-7"></a> [selection_test.go](../../internal/terminal/selection_test.go#L1-L123) — native text, history/reflow, screen invalidation and eviction contracts.
8. <a id="source-8"></a> [selection_test.go](../../internal/tui/term/selection_test.go#L1-L37) — held-output selection and copy regression.
9. <a id="source-9"></a> [Clipboard boundary](clipboard.md) — outer OSC 52 validation, bounds and opt-out.
10. <a id="source-10"></a> [Ghostty Config.zig at the pinned core revision](https://github.com/ghostty-org/ghostty/blob/33da6848d63b3bba2b4f31ab1531d618f2795192/src/config/Config.zig#L6642-L6656) — default platform copy chord with `performable` flag.
11. <a id="source-11"></a> [Ghostty performable keybindings](https://ghostty.org/docs/config/keybind#performable) — conditional key consumption, consulted 2026-10-02.
12. <a id="source-12"></a> [Codex local_settings.rs at rust-v0.162.1](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/tui/src/local_settings.rs#L200) — automatic copy preferences.
13. <a id="source-13"></a> [Codex terminal detection at rust-v0.162.1](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/terminal-detection/src/lib.rs#L248) — terminal environment detection.
14. <a id="source-14"></a> [identity.go](../../internal/termio/identity.go#L5) — bounded metadata and confined child argv.
15. <a id="source-15"></a> [controller_client.go](../../cmd/yoloctl/controller_client.go#L351) — host request metadata.
16. <a id="source-16"></a> [session_client.go](../../cmd/yoloctl/session_client.go#L378) — holder argv and `sessionTerminalIdentity`.
17. <a id="source-17"></a> [controller_view_test.go](../../cmd/yoloctl/controller_view_test.go#L98) — wire preservation and control rejection.
18. <a id="source-18"></a> [identity_test.go](../../internal/termio/identity_test.go#L10) — literal argv execution.
19. <a id="source-19"></a> [TUI panes](tui-panes.md) — selection kept inside its pane in a divided tab.
