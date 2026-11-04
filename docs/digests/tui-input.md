# TUI routing, keyboard, mouse, and paste

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `opensLiveSurface` routes box attach, exec/edit, space shells, catalog
editors and following logs to tabs; lifecycle commands use attachment flags.
There is no universal help override. Dispatch executes the current binary.
[1](#source-1)

[FACT] Palette editing converts whitespace to spaces and drops other terminal
controls, preserving its single-row layout and child cursor alignment. It
submits `strings.Fields` argv without shell interpretation. Completion runs the
same binary's Cobra `__complete`, parses descriptions/directives and uses a
two-second deadline. [2](#source-2) [3](#source-3) [20](#source-20)

[FACT] The palette ends with the bold, right-aligned `(space)` label. Command
echoes omit the working directory. Busy commands replace the left `yoloctl`
brand with a fixed-width ASCII wave; right-hand status and input do not move.
Width budgeting reserves a gap and trailing paint column to prevent wrapping.
[21](#source-21) [22](#source-22)

[FACT] The tab strip removes one exact leading `yoloctl — ` from displayed
titles before icon spacing and truncation. The window title removes every exact
leading copy, then adds one; no tab or an empty or `yoloctl`-only remainder
(including a trimmed `yoloctl —`) gives `yoloctl`. Neither changes interior
text, case variants, other dashes or the stored tab/application title, and
re-wrapping a nested window title is idempotent. [23](#source-23)
[24](#source-24) [25](#source-25) [26](#source-26)

[FACT] Palette Tab accepts suggestions with Caps/Num Lock state. Tests cover
raw input through Bubble Tea, including releases. [7](#source-7)
[9](#source-9)

[FACT] A live tab reserves the configured palette chord and explicit copy of an
existing local selection. Enabled managed PNG paste can consume Ctrl+V and
insert a guest path through the ordered queue. Other keys reach its terminal encoder; the palette
owns tab selection/closing and history. A running
overlay sends Ctrl+C to its command; a second within two seconds kills the local
command group. Leaving the overlay does not stop that command. [4](#source-4)
[15](#source-15)
[19](#source-19)

[FACT] Private attachment failures appear as wrapped palette results. A busy
palette command delays their display; background failures mark their tabs
without stealing focus. Standalone attachment retains its error notice and
returned diagnostic. [6](#source-6) [17](#source-17)

[FACT] Host Bubble Tea keyboard enhancements request event types, alternate
keys, all keys and associated text. The core owns child Kitty negotiation and
screen stacks; a Go encoder preserves the host's logical key/text and all eight
modifier bits. Release events go to the terminal which received their press,
even after focus changes; palette-owned releases are consumed. This cannot
recover events or alternate keycodes the host never reported. [5](#source-5)
[6](#source-6) [13](#source-13)

[FACT] With no child Kitty flags, the native core encoder follows its legacy
modes, including application cursor and modifyOtherKeys. Distinguishable
Control combinations may use CSI-u in that profile. Bare Enter is CR; the
adapter does not turn every modified Enter into LF. The pinned Ultraviolet
functional-key decoder leaves Kitty Meta/Super positions unconverted; a
support-aware workaround normalizes them, with an independent byte oracle.
[5](#source-5) [7](#source-7) [8](#source-8)

[FACT] Host LF decodes as Ctrl+J, distinct from CR/Enter. With restored Kitty
flags 3, Ctrl+J becomes `CSI 106;5u` while bare Enter remains CR. The PTY smoke
sends CR to submit shell commands; LF is not a mode-independent Enter shortcut.
[7](#source-7) [16](#source-16)

[FACT] Keyboard tests cover 11 forms × 256 modifiers × 3 event types, explicit
bytes, negotiation, ownership and legacy modes. Dependency regressions cover
combining text, OSC 8 and host keyboard-stack restoration. This is automated
coverage, not native terminal acceptance. [7](#source-7) [8](#source-8) [9](#source-9)

[FACT] Committed IME text has separate real reader/PTY and palette coverage.
Kitty text-only key 0 preserves its payload, independently of Ctrl+Space.
[18](#source-18)

[FACT] Paste routes to the busy overlay, editable palette or live tab; child
mode controls bracketed wrapping. A bounded queue reports rejection and
transport errors instead of waiting for the PTY reader. Resize reaches child
surfaces; measured host default colors reach emulators. [6](#source-6)

[FACT] Host focus reports and tab/palette ownership determine which child has
focus. Mode 1004 gates child reports; its observed initial state and subsequent
changes use the ordered input queue, with duplicates suppressed. A real local
PTY regression covers host blur/refocus and palette transitions. [14](#source-14)

[FACT] Mouse gestures retain their original application or chrome owner through
focus/size changes. The native encoder applies effective child mouse modes;
busy palette commands also receive live-grid input. Alternate-screen wheel
events become application-cursor-aware arrows when mode 1007 requests it;
otherwise the TUI scrolls history. Tab content without application mouse input
supports local selection; host selection/copy remains independent. [10](#source-10)
[15](#source-15)

[FACT] Inside a live child only two keys are reserved: the palette key and
the pane prefix (`Ctrl+Q`), which reaches pane and tab actions from every
focus [27](#source-27).

[FACT] Defaults: Ctrl+Y palette, Ctrl+Q pane prefix, 10,000 scrollback lines, bounded palette height.
`LoadTUI` writes missing defaults and uses non-strict YAML decoding. Input debug
uses `YOLOCTL_TUI_DEBUG` and `YOLOCTL_TUI_DEBUG_LOG`. [11](#source-11)
[12](#source-12)

## Sources

1. <a id="source-1"></a> [tui.go](../../internal/tui/tui.go#L466) — `opensLiveSurface`, dispatch and `openTab`.
2. <a id="source-2"></a> [palette.go](../../internal/tui/palette/palette.go#L25) — `Insert`, `set`, `singleLine`, `Submit`, `CompletionInput`.
3. <a id="source-3"></a> [complete.go](../../internal/tui/yoloctlcli/complete.go#L1-L100) — `Complete` and result parsing.
4. <a id="source-4"></a> [tui.go](../../internal/tui/tui.go#L1202) — `handleKey`, `handleRunningKey`, tab close.
5. <a id="source-5"></a> [terminal_keys.go](../../internal/tui/terminal_keys.go#L1-L294) — host normalization, `sendKey`, `childKey`, `kittyKey` and functional mapping.
6. <a id="source-6"></a> [tui.go](../../internal/tui/tui.go#L746) — `Update`, release/paste routing, `sendInput`, input notices and `reportViewErrors`.
7. <a id="source-7"></a> [terminal_keys_test.go](../../internal/tui/terminal_keys_test.go#L1) — negotiated bytes, restored Enter/Ctrl+J, modifier matrix, release ownership, independent modifier oracle and palette Tab reports.
8. <a id="source-8"></a> [keys_test.go](../../internal/tui/keys_test.go#L1-L122) — legacy decode/encode, Control text, reserved chords and DECCKM.
9. <a id="source-9"></a> [dependencies_test.go](../../internal/tui/dependencies_test.go#L1-L136) — pinned host decoder/renderer/keyboard-stack regressions and palette input loop.
10. <a id="source-10"></a> [Mouse delivery and selection boundaries](terminal-mouse.md) — native encoding, gesture ownership, console coordinates and selection limits.
11. <a id="source-11"></a> [tui.go](../../internal/config/tui.go#L1-L339) — configuration, commented defaults and load.
12. <a id="source-12"></a> [debug.go](../../internal/tui/debug.go#L1-L28) — opt-in input diagnostics.
13. <a id="source-13"></a> [tui.go](../../internal/tui/tui.go#L1594) — `View`, host keyboard enhancement requests.
14. <a id="source-14"></a> [Display snapshots, history and focus](terminal-display.md) — focus state, mode gating and input-delivery regressions.
15. <a id="source-15"></a> [Native selection and explicit copy](terminal-selection.md) — local selection and conditional copy-key ownership.
16. <a id="source-16"></a> [box-attach.sh](../../scripts/smoke/box-attach.sh#L15) — scripted Enter bytes in `pty_run` and shared detach/reattach input fixtures.
17. <a id="source-17"></a> [view_errors_test.go](../../internal/tui/view_errors_test.go#L93) — inherited-channel rejection, palette wrapping/defer, background focus and standalone diagnostics.
18. <a id="source-18"></a> [Compatibility evidence](terminal-compatibility.md) — legacy input, committed text, focus/cursor coverage and limits.
19. <a id="source-19"></a> [Host integrations](host-integrations.md) — explicit PNG paste ownership and live browser approval.
20. <a id="source-20"></a> [palette_paste_test.go](../../internal/tui/palette_paste_test.go#L13) — host-reader paste/focus and cursor alignment regression.
21. <a id="source-21"></a> [tui.go](../../internal/tui/tui.go#L1994) — `inputLine`, `contextChip` and `promptLine`.
22. <a id="source-22"></a> [tui_test.go](../../internal/tui/tui_test.go#L839) — wave width, context position and command echo regressions.

23. <a id="source-23"></a> [Tab labels](../../internal/tui/tui.go#L2351) — `tabStripLabels`.
24. <a id="source-24"></a> [Tab title regressions](../../internal/tui/microfix_ui_test.go#L12) — `TestTabStripStripsOnlyExactLeadingYoloctlPrefix`.
25. <a id="source-25"></a> [Window title](../../internal/tui/tui.go#L1767-L1792) — `View` title, `titleWrapper` and `windowTitle`.
26. <a id="source-26"></a> [Window title regressions](../../internal/tui/microfix_ui_test.go#L43) — `TestWindowTitleCollapsesOnlyExactGeneratedPrefixes`.
27. <a id="source-27"></a> [TUI pane prefix](tui-pane-prefix.md) — precedence and pane keys.
