# Display snapshots, history and focus

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] A display frame captures styled viewport rows, cursor coordinates,
shape/blink, history count and scroll position under one emulator lock. Each
TUI `View` captures one frame per surface; layout and cursor helpers use those
same frames. Snapshot slices belong to their caller. Exported rows are bounded
by viewport height rather than total history. [1](#source-1) [2](#source-2)
[3](#source-3) [10](#source-10)

[FACT] Scrolling anchors the viewport to a native tracked text position, which
follows output and reflow. Coordinates account for the adapter's visible
history cap. An evicted or out-of-cap anchor clamps to the oldest surviving
row. Reset, a screen switch or a return to live output clears the anchor.
The console trims trailing text-blank grid rows. [1](#source-1) [4](#source-4)
[5](#source-5)

[FACT] Synchronized output (mode 2026) captures the display at the native hold
boundary, including when a completed frame and another hold share one PTY
read. Release, reset, resize, child completion or a one-second timeout publishes
the accumulated state. The timer wakes the renderer even without new output;
an epoch prevents an old timer from releasing a later hold. Parsing and query
replies continue during a hold. Managed replicas receive the holder's ordered
timeout event instead of independently changing canonical mode state. [2](#source-2) [4](#source-4) [6](#source-6)
[7](#source-7) [12](#source-12)

[FACT] Grid and history exports retain native text/style formatting. The
bridge adds cell OSC 8 hyperlink runs because the pinned core's VT formatter
omits them. It retains explicit IDs and percent-encodes control/non-ASCII bytes
and ID parameter separators. Rendering does not forward raw application modes,
queries or clipboard controls. Regressions check attributes, hyperlinks and
grapheme columns after history scrolling. [4](#source-4) [6](#source-6)

[FACT] Native row formatting can omit empty trailing columns. The palette pads
those columns by display width before applying its background, including blank
and historical rows; explicit child cell backgrounds remain visible.
[10](#source-10) [17](#source-17)

[FACT] The configured default is a blinking block cursor; child bar, block,
underline and blink settings reach the host cursor. A hollow block maps to the
host's standard block. The TUI requests host focus reports and assumes focus
until a blur arrives. Children opting into mode 1004 receive their observed
initial state and host/tab/palette focus changes through the ordered input
queue; duplicate states and unrequested reports are suppressed. [3](#source-3)
[4](#source-4) [5](#source-5) [8](#source-8) [13](#source-13)
[14](#source-14)

[FACT] Tests establish these bounded contracts, not native graphical parity.
Host palette inheritance and live bell delivery have separate bounded evidence.
[18](#source-18) Display snapshots are local rendering state, not persistent
session screen checkpoints. [6](#source-6) [9](#source-9) [11](#source-11)
[15](#source-15) [16](#source-16)

## Sources

1. <a id="source-1"></a> [frame.go](../../internal/terminal/frame.go#L1-L139) — `Frame`, `Scroll`, `historyBase` and native anchor ownership.
2. <a id="source-2"></a> [frame.go](../../internal/tui/term/frame.go#L1-L102) — frame locking, boundary capture, timer epochs and `EndRenderHold`.
3. <a id="source-3"></a> [frames.go](../../internal/tui/frames.go#L1-L113) — shared frame helpers, cursor composition and `syncFocus`.
4. <a id="source-4"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L294-L409) — tracked references, hold release and hyperlink row formatting; native callbacks and defaults precede these exports.
5. <a id="source-5"></a> [frame_test.go](../../internal/terminal/frame_test.go#L1-L91) — anchor eviction, reflow, reset, alternate screen, trimming and cursor styles.
6. <a id="source-6"></a> [frame_test.go](../../internal/tui/term/frame_test.go#L1-L148) — hold boundaries, silent timeout, styles/links and concurrent grid/cursor regressions.
7. <a id="source-7"></a> [session.go](../../internal/tui/term/session.go#L187) — release of a hold on session completion.
8. <a id="source-8"></a> [modes.go](../../internal/tui/term/modes.go#L20-L44) — `UpdateFocus` and mode gating.
9. <a id="source-9"></a> [terminal.go](../../internal/terminal/terminal.go#L46-L78) — event storage.
10. <a id="source-10"></a> [tui.go](../../internal/tui/tui.go#L1491) — `View`, overlay padding and cursor placement.
11. <a id="source-11"></a> [frames_test.go](../../internal/tui/frames_test.go#L1-L72) — coherent TUI view and anchored history regressions.
12. <a id="source-12"></a> [console.go](../../internal/tui/term/console.go#L120-L149) — release of a hold on console completion.
13. <a id="source-13"></a> [focus_test.go](../../internal/tui/term/focus_test.go#L1-L33) — requested reports and deduplication.
14. <a id="source-14"></a> [focus_test.go](../../internal/tui/focus_test.go#L1-L54) — real local PTY focus delivery.
15. <a id="source-15"></a> [handler.go](../../internal/tui/term/handler.go#L10) — default color handling.
16. <a id="source-16"></a> [Session replay](session-replay.md) — persistent replay boundary.
17. <a id="source-17"></a> [palette_render_test.go](../../internal/tui/palette_render_test.go#L1-L66) — per-cell background assertions over blank, styled, linked, Unicode and historical output.
18. <a id="source-18"></a> [Host colors and live terminal bells](terminal-colors-bells.md) — palette inheritance, isolated overrides and event delivery.
