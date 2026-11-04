# Mouse delivery and selection boundaries

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] A mouse press records its owner per button. Chrome-owned gestures consume
their drag and release. Application gestures keep their original surface across
tab changes, palette dismissal and resize; cell formats clamp out-of-bounds
coordinates, while pixel reports retain signed positions. A closed owner never redirects its release to another child.
A pane owner's row and column origin follows its pane [22](#source-22); strip,
bar and border presses are chrome gestures that capture their objects [23](#source-23).
Legacy host releases without a button complete the recorded owners in button
order. A drag without an observed press is not sent to a child. [1](#source-1)
[4](#source-4)

[FACT] The native core encodes cell-based X10, normal, UTF-8, SGR and urxvt mouse
reports, applying its effective tracking/format state and event filtering.
Buttons 10/11 retain their gesture owners; a narrow native patch supplies their
standard codes 130/131 and UTF-8 encodes the button field in mode 1005.
[13](#source-13)
Effective enums matter: resetting a mode can change the active behavior even
when another stored DEC bit remains set. Mode 1016 needs measured host and
canonical geometry; missing measurements produce a visible error.
Encoding and enqueueing share the emulator lock with output mode changes.
[2](#source-2) [3](#source-3) [5](#source-5) [10](#source-10)

[FACT] A running palette command can receive mouse events over its displayed
live grid. The frame's live-grid origin accounts for trimmed output and
history. Historical rows are not sent as current application coordinates.
The host requests all-motion reporting for a focused child asking for it,
including a busy palette command; otherwise it requests cell motion for chrome
and drag handling. [1](#source-1) [6](#source-6) [7](#source-7)

[FACT] The adapter owns host tracking/encoding together; Bubble Tea's View
must not overwrite it with cell SGR. Pixel mode needs measured cells and
advertised host support. Tracking pauses during a units change; DECRPM on the
input stream acknowledges the boundary. Captured gestures defer unit changes.
Unconfirmed transitions disable reporting after two seconds with a visible
notice; reopening restores it. Exit clears tracking and encoding.
[20](#source-20) [21](#source-21)

[FACT] Wheel events reach a mouse-reporting application first. Without mouse
reporting, alternate-screen mode 1007 translates vertical wheel events to
application-cursor-aware arrows. Remaining vertical wheel events scroll local
history by one row. [1](#source-1) [6](#source-6)

[FACT] Mouse-reporting applications retain their selection input. In a tab
without application mouse reporting, or while viewing its history, plain drag
selects local text using native pins. The palette does not implement local
selection. Host selection/copy bindings remain outside yoloctl; Ghostty documents
Shift as its default reporting override, with configurable exceptions. The
adapter propagates the focused reporting child's XTSHIFTESCAPE preference,
including a busy console. Captured gestures retain Shift reporting through
focus changes until release. Background/non-reporting children cannot request
host capture. Commands serialize preference changes; palette/history focus and
exit return to selection override after a capture request. Host forced
`always`/`never` settings still win. XTSHIFTESCAPE cannot restore an unspecified
preference: after touching it, cleanup sends zero. [14](#source-14) [17](#source-17)
[1](#source-1) [6](#source-6) [8](#source-8) [9](#source-9) [12](#source-12)

[FACT] Native snapshots preserve the three-way Shift preference. Compatibility
replay restores explicit capture, otherwise selection override, including after
ring eviction and fragmented controls. Session-end cleanup disables all tracked
mouse encodings, including pixels, and restores Shift-selection.
[15](#source-15) [16](#source-16) [18](#source-18) [19](#source-19)

[FACT] Tests assert independent protocol bytes and use real local PTY peers to
record routing, missing reports, multiple buttons, closed owners, tab changes,
resize and short/history-filled busy consoles. Byte expectations follow xterm
reporting semantics. [11](#source-11) They do not establish graphical selection
parity in native Ghostty/Kitty. [4](#source-4) [5](#source-5)

## Sources

1. <a id="source-1"></a> [mouse.go](../../internal/tui/mouse.go#L1-L279) — target mapping, gesture ownership, delivery and wheel routing.
2. <a id="source-2"></a> [mouse.go](../../internal/terminal/mouse.go#L58) — native encoding, measured pixel scaling and invalid-input rejection.
3. <a id="source-3"></a> [mouse.go](../../internal/tui/term/mouse.go#L1-L31) — serialization, ordered input and closed-owner handling.
4. <a id="source-4"></a> [mouse_test.go](../../internal/tui/mouse_test.go#L1-L233) — real PTY gesture and console tests.
5. <a id="source-5"></a> [mouse_test.go](../../internal/terminal/mouse_test.go#L1) — independent wire bytes, pixel rejection and native Shift-state/snapshot tests.
6. <a id="source-6"></a> [tui.go](../../internal/tui/tui.go#L755) — `Update` host acknowledgments; `View` leaves mouse encoding to the adapter.
7. <a id="source-7"></a> [frame.go](../../internal/terminal/frame.go#L18-L88) — `Frame.GridStart` and visible history/grid coordinates.
8. <a id="source-8"></a> [Ghostty configuration: mouse-shift-capture](https://ghostty.org/docs/config/reference#mouse-shift-capture) — host override semantics, consulted 2026-10-06.
9. <a id="source-9"></a> [Ghostty XTSHIFTESCAPE](https://ghostty.org/docs/vt/csi/xtshiftescape) — application's request versus host policy, consulted 2026-10-06.
10. <a id="source-10"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L532) — `yolo_vt_mouse` and the pinned native encoder.
11. <a id="source-11"></a> [xterm mouse tracking](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html#h2-Mouse-Tracking) — protocol byte definitions, consulted 2026-10-02.
12. <a id="source-12"></a> [Native selection and explicit copy](terminal-selection.md) — local selection eligibility, text and host-copy boundary.
13. <a id="source-13"></a> [native.patch](../../internal/terminal/wasm/native.patch#L1) — native extra-button and UTF-8 field fixes.
14. <a id="source-14"></a> [host_mouse.go](../../internal/tui/host_mouse.go#L10) — preference/gesture selection, serialized host commands and cleanup.
15. <a id="source-15"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L601) — `inputReplay` Shift preference and continuation.
16. <a id="source-16"></a> [reset.go](../../internal/termio/reset.go#L34) — session cleanup.
17. <a id="source-17"></a> [host_mouse_test.go](../../internal/tui/host_mouse_test.go#L25) — delayed transitions, focus, real renderer and PTY input.
18. <a id="source-18"></a> [terminal_state_test.go](../../internal/session/terminal_state_test.go#L29) — ring/chunk/continuation comparison of effective Shift state.
19. <a id="source-19"></a> [reset_test.go](../../internal/termio/reset_test.go#L11) — native cleanup checks.
20. <a id="source-20"></a> [host_mouse_encoding.go](../../internal/tui/host_mouse_encoding.go#L10) — serialized tracking, pixel acknowledgment and gesture ownership.
21. <a id="source-21"></a> [Measured geometry and pixel input](terminal-geometry.md) — coordinate units, geometry transport, decoder patch, tests and compatibility limits.
22. <a id="source-22"></a> [TUI panes](tui-panes.md) — pane owners, column offsets, focus on press.
23. <a id="source-23"></a> [TUI pane chrome](tui-pane-chrome.md) — chrome gestures, release activation, resize drags.
