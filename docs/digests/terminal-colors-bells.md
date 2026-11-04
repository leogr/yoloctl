# Host colors and live terminal bells

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Formatted rows retain unmodified indexed foreground, background and
underline colors as SGR indices, inheriting the host's theme even when it
does not answer color queries. Only application-modified palette entries become
RGB. The native formatter emits canonical individual SGR tokens; the adapter
replaces those tokens using the native override mask. Rendering never sends
OSC palette mutations to the host. Existing history uses the current virtual
palette, and independent terminals have independent overrides.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The pinned host renderer has a narrow local patch: its style comparison
distinguishes palette indices from other indices and RGB values. Basic/indexed
spellings of the same entry remain equivalent. This prevents foreground,
background and underline transitions or color-only redraws from disappearing
when the upstream library's built-in RGB values happen to match. The copy
reproduces from the checksum-pinned module plus its isolated patches.
[16](#source-16) [17](#source-17) [18](#source-18)

[FACT] Startup and host focus request all 256 palette entries with individual
OSC 4 queries. Valid reports update cached defaults for the console, existing
tabs and subsequently opened tabs. Parsing accepts bounded, complete RGB reports
with validated indices/channels and rejects an entire malformed batch.
[1](#source-1) [4](#source-4) [5](#source-5) [6](#source-6) [12](#source-12)

[FACT] Both host TUI programs install one event filter for these reports. It
runs on Bubble Tea's event loop before Update, so a consumed report costs no
Update or View. Reports collect into one batch, admitted when all 256 indices
have answered or at a fixed 100 ms deadline armed by the batch's first report.
Further reports join without extending it; reports after a flush open the next
batch, and a stale deadline never flushes it. Each surface then re-applies host
colors once per batch. Malformed, query-shaped and unrelated OSC events and all
other input pass unchanged and in order. Standalone `yoloctl chat` sends no
palette probe. [4](#source-4) [5](#source-5) [20](#source-20) [21](#source-21)
[22](#source-22) [23](#source-23)

[FACT] Native default changes preserve application overrides; OSC 104 and RIS restore
the measured defaults. This uses xterm's query/report convention.
[1](#source-1) [12](#source-12)

[FACT] Unknown host colors do not produce invented OSC 4 replies. The adapter
keeps at most one unanswered query per index, retaining its BEL/ST terminator.
A measured report releases the pending reply using the effective native color;
an explicit child override is immediately queryable. Clearing the console drops
the previous command's pending queries. Host updates never inject queries into
unfinished child controls. Native replies can contain multiple reports in one
callback. Foreground/background OSC 10/11 defaults retain their separate bounded
observer. The holder and ordinary local PTYs share this helper; managed
replicas suppress replies and send measurements to the holder. [7](#source-7)
[8](#source-8) [19](#source-19)

[FACT] The native parser identifies executed bells; OSC terminators and string
payloads are not treated as bell events. The emulator invokes its handler outside
the mutex, once per output batch. Tabs and console send BEL through Bubble Tea's
serialized host writer, leaving audible/visual/disabled behavior to the host.
Drawing, scrolling, resetting or switching tabs does not replay those events.
[9](#source-9) [10](#source-10)

[FACT] Native managed snapshots restore quietly; future native bell actions
become live frontend events. Compatibility replay separately neutralizes bells:
a native stream replaces executed BEL with NUL, preserving UTF-8 interruption
boundaries. Its input-state terminal models only the evicted transcript prefix.
The compatibility control filter omits queries and opaque extensions before
retention. Existing holders need a fresh agent session to adopt changed behavior.
[1](#source-1) [11](#source-11) [13](#source-13) [19](#source-19)

[FACT] Regressions cover themes, overrides/reset/history, delayed replies,
fragmented controls, invalid reports, palette-report batching through the real
Bubble Tea input loop, partial-reply deadlines, real local PTY bell delivery,
the actual Bubble Tea output loop, and holder/SSH reattachment with live continuation.
Native stream tests compare filtered/unfiltered screen, title, replies and bell
counts across Unicode, invalid UTF-8, control strings and write boundaries.
They do not establish graphical theme parity, host audio/notification behavior
or live Lima validation. [3](#source-3) [8](#source-8) [10](#source-10)
[14](#source-14) [15](#source-15) [20](#source-20)

## Sources

1. <a id="source-1"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L176-L395) — `yolo_vt_palette_color` defaults, `BellStream` and the override-mask row formatting.
2. <a id="source-2"></a> [palette.go](../../internal/terminal/palette.go#L1-L55) — `SetPaletteColor`, native override state and canonical SGR replacement.
3. <a id="source-3"></a> [palette_test.go](../../internal/terminal/palette_test.go#L1-L68) — inherited indices, local overrides, history and resets.
4. <a id="source-4"></a> [colors.go](../../internal/tui/colors.go#L16-L122) — `requestHostPalette`, `programOptions`, `filterHostPalette`, `flushHostPalette`, `applyHostPalette`.
5. <a id="source-5"></a> [tui.go](../../internal/tui/tui.go#L876-L1001) — new-tab `Palette`, `FocusMsg` refresh, `hostPaletteStart`/`hostPaletteFlush` and unfiltered reports.
6. <a id="source-6"></a> [colors.go](../../internal/termio/colors.go#L1-L71) — strict bounded `ParsePaletteReport`.
7. <a id="source-7"></a> [colors.go](../../internal/terminal/colors.go#L54-L162) — `ColorQueries`: grouped native replies, deferred queries and measured defaults (`Reply`, `Apply`).
8. <a id="source-8"></a> [palette_test.go](../../internal/tui/term/palette_test.go#L1-L65) — delayed/fragmented queries and console clearing.
9. <a id="source-9"></a> [emulator.go](../../internal/tui/term/emulator.go#L69-L111) — `writeOutput`: counted native events and unlocked callbacks.
10. <a id="source-10"></a> [bell_test.go](../../internal/tui/bell_test.go#L1-L48) — bells through the real Bubble Tea writer.
11. <a id="source-11"></a> [bell.go](../../internal/terminal/bell.go#L1-L32) — separate stream ownership and bounded filtering.
12. <a id="source-12"></a> [xterm operating-system commands](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html#h2-Operating-System-Commands) — OSC 4 queries/reports and reset controls, consulted 2026-10-02.
13. <a id="source-13"></a> [terminal_state.go](../../internal/session/terminal_state.go#L1-L90) — filter errors, evicted-prefix ownership and replay admission.
14. <a id="source-14"></a> [bell_test.go](../../internal/terminal/bell_test.go#L1-L66) — native classification and Unicode boundaries.
15. <a id="source-15"></a> [holder_test.go](../../internal/session/holder_test.go#L556) — `TestHolderBellsAreLiveOnly`: quiet replay with unchanged live bells and titles.
16. <a id="source-16"></a> [cell.go](../../third_party/ultraviolet/cell.go#L416-L446) — palette-aware `colorEqual` and `paletteIndex`.
17. <a id="source-17"></a> [palette_identity_test.go](../../internal/tui/palette_identity_test.go#L14-L97) — actual renderer transitions and color-only repaint.
18. <a id="source-18"></a> [vendor-ultraviolet.py](../../scripts/vendor-ultraviolet.py#L1-L55) — module pin, checksum, isolated patches and reproduction comparison.

19. <a id="source-19"></a> [Canonical session state](session-replay.md) — query/color ownership, native snapshots and compatibility replay.
20. <a id="source-20"></a> [colors_test.go](../../internal/tui/colors_test.go#L66-L249) — `TestHostPaletteBurstThroughHostInputLoop`, `TestHostPalettePartialRepliesFlushAtFixedDeadline`.
21. <a id="source-21"></a> [chat_view.go](../../cmd/yoloctl/chat_view.go#L111) — standalone chat program without the host palette probe or focus reports.
22. <a id="source-22"></a> [tui.go](../../internal/tui/tui.go#L162-L173) — `Run` builds its program with `programOptions`.
23. <a id="source-23"></a> [attach.go](../../internal/tui/attach.go#L18-L26) — `RunAttached` uses the same options.
