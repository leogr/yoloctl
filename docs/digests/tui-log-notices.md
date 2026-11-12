# TUI log notices

Verification: `b3b2cf92659f79545ec4cc8d405d5f1b0a3c122e` and accompanying changes.

[FACT] Host-owned TUI status messages, pane-menu refusals and terminal exit
notices use `prettylog` kinds, glyphs and bounded message escaping. Errors use
`✗`, warnings `!`, notices `•`, successes `✓` and unknown outcomes `?`.
Direct-action feedback is displayed at every command-log level; choosing
`--log-level error` does not hide why a pane action was refused. Command output
retains the child logger's filtering. [1](#source-1) [2](#source-2) [4](#source-4)

[FACT] The outer TUI reports its existing background-colour response through
`logtheme.SetBackground`; it adds no terminal query or input reader. Existing
and newly launched console/pane terminal notices use the dashboard theme's
semantic roles. `--color` and automatic `NO_COLOR`/non-terminal colour policy
apply to these notices. Native space-view toasts use the same glyph vocabulary
and theme roles; a low-disk warning uses `Warn`, critical uses `Bad`.
[3](#source-3) [4](#source-4) [5](#source-5) [6](#source-6) [9](#source-9) [10](#source-10) [11](#source-11)

[FACT] Palette commands still use the child PTY and embedded terminal. The
host does not parse rendered command output into another log stream. After
the old command's I/O pumps join, the console cancels an unfinished parser
string before appending its host exit notice; an uncoloured notice cannot
be swallowed by an incomplete child OSC. Before the next prompt, `Console.Run`
uses the qualified native history-preserving reset: earlier primary rows and
scrollback remain under the existing retention limit, while terminal/parser
and Go-side query/focus/clipboard state start fresh. No rows are exported or
replayed and no disk log is added. Local errors also append through this reset.
Palette dismissal, focus changes and suggestion replacement hide or replace
the display without erasing history; showing the console again restores it. [4](#source-4) [7](#source-7) [8](#source-8)
[12](#source-12) [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [Notice records](../../internal/tui/log_notices.go#L19) — `noticeRecord`, `noticeLineOptions`, `setStatus`, `statusLine`, `noticeFooter`.
2. <a id="source-2"></a> [Notice regressions](../../internal/tui/log_notices_test.go#L23) — error-level refusal, escaping, bounds, shared light/dark theme.
3. <a id="source-3"></a> [TUI](../../internal/tui/tui.go#L289) — `updateTerminalColors`, `startPane`, background response, `inputLine`.
4. <a id="source-4"></a> [Terminal notices](../../internal/tui/term/log_notices.go#L16) — `SetLogPalette`, `logNoticeOptions`; console.go `Run`, `ShowError`, `pump`; session.go `writeExitBanner`.
5. <a id="source-5"></a> [Toast roles](../../internal/tui/spaceview/log_notices.go#L5) — `Theme.LogTone`.
6. <a id="source-6"></a> [Toast regressions](../../internal/tui/spaceview/log_notices_test.go#L10) — semantic roles in both themes.
7. <a id="source-7"></a> [Parser boundary](../../internal/tui/term/lifecycle_test.go#L90) — `TestConsoleReuseExcludesRetainedDescendantOutput`.
8. <a id="source-8"></a> [Terminal feedback](../../internal/tui/term/log_notices_test.go#L14) — shared palette, outcomes and escaping.
9. <a id="source-9"></a> [Toast model](../../internal/tui/spaceview/model.go#L347) — `say` and event classification.
10. <a id="source-10"></a> [Toast rendering](../../internal/tui/spaceview/layout.go#L328) — shared glyphs and semantic tone.
11. <a id="source-11"></a> [Disk warnings](../../internal/tui/spaceview/disks.go#L204) — `diskEvents`.
12. <a id="source-12"></a> [History-preserving reset](../../internal/tui/term/emulator.go#L226) — `ClearKeepHistory`, `reset`; native `Terminal.ResetKeepHistory`.
13. <a id="source-13"></a> [Consecutive-command regression](../../internal/tui/term/console_test.go#L57) — retained prompts/output/exit notices and fresh input modes; lifecycle_test.go retains old direct-child text but rejects the late descendant's output and clipboard tail.
14. <a id="source-14"></a> [History-preserving UI transitions](../../internal/tui/tui.go#L1603) — `resetConsoleView`, `leavePaletteForTab`, `refreshSuggestions`.
