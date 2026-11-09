# Native chat width methods

Verification: `d9aec4ac854ffd5b4f60a93817b8c5afce10847d` and accompanying changes.

[FACT] Each chat Model owns one `ansi.Method`, initially its zero value,
`WcWidth`. A `ModeUnicodeCore` report with Reset, Set or PermanentlySet changes
it to `GraphemeWidth`; other modes and values do nothing. Repeated accepted
reports are idempotent and ignored reports never revert it. This mirrors the
existing TUI predicate; Bubble Tea owns terminal negotiation, with no second
query/set loop in chat. The pins are Bubble Tea v2.0.10 and x/ansi v0.11.8.
[1](#source-1) [2](#source-2)

[FACT] Pure geometry helpers receive the method explicitly. Row painting,
wrapping, truncation at both ends, grapheme hit ranges and actions use it,
as do tables' natural widths, column floors, budgets and cell wrapping,
code-card widths, tab expansion and source maps, draft wrapping and cursor
movement. Model-owned chrome, icons, roster, previews, overlays, browser prompt
and marquee use the same method. Overlay button budgets include separation
before subsequent buttons, so a trailing gap cannot overflow the card. There is
no package-wide width setting.
[3](#source-3) [4](#source-4) [5](#source-5)

[FACT] Local ANSI-aware truncation decodes with the selected method. It avoids
x/ansi v0.11.8's grapheme-only early-return check in `Method.Truncate`, which
can leave a long ZWJ string wider than a wcwidth budget. Left clipping replaces
the uncovered part of a crossing wide cluster with spaces. The decoder uses
the renderer's ASCII-plus-combining correction and preserves style controls.
[10](#source-10)

[FACT] A real change clears every channel's body cache (including aligned
lines and expanded tabs), built transcript, last transcript, draft rows and
preferred editor column, and advances its transcript generation. A reading
anchor retains message and logical line but resets its old display-byte offset.
Draft text/cursor bytes, reply, messages and follow state stay intact. Highlights
clear, active gestures cancel, and stale drag/marquee ticks cannot continue.
The eventual release does nothing. Hover and hits reset; the current view
rebuilds synchronously before another mouse event can use its coordinates.
[6](#source-6)

[FACT] Source/code copy and outgoing text retain original bytes. Display copy
keeps the current tab expansion; previews still copy their reference token.
Quote rebuilds its source maps with the same method, restoring original markup
and tabs where exact, or quoting selected table cell text under its existing
contract. [4](#source-4) [7](#source-7)

[FACT] Fixed metadata safety truncation remains wcwidth-based for runtime
values/notes and retained image-upload/staging errors. Their subsequent layout
uses the model's method. The editor's boundary-only iterator discards measured
widths and stays independent of negotiation. These sites neither allocate
screen columns nor determine hit coordinates. [8](#source-8)

[FACT] Component regressions compose the screenshot-derived VS16 table and prose
beside Participants through ultraviolet's styled-string cell buffer, showing the
one-cell displacement with mismatched methods and aligned dividers with matched
methods. Mode/value, symbol, wide/narrow table, overlay/browser, cursor, hit,
copy/Quote, inactive-channel and late-report tests cover both methods. This
buffer evidence establishes neither host font rendering nor graphical parity;
plain text-presentation ⏸ remains a separate host-observation boundary.
[9](#source-9)

## Sources

1. <a id="source-1"></a> [Model method and reports](../../internal/chatui/model.go#L270-L274) — `widthMethod`; `update` mode predicate at L420-L425.
2. <a id="source-2"></a> [TUI predicate](../../internal/tui/tui.go#L923-L930); pins in `go.mod:L13-L16`.
3. <a id="source-3"></a> [Row geometry](../../internal/markdownview/rows.go#L36) — `Paint`, `RunsWidth`, `WrapRanges`, `HitRun`, `ActionAt`.
4. <a id="source-4"></a> [Shared bodies/tables](../../internal/markdownview/body.go#L101) — `parseLines`, `TableLines`, `FitTable`; shared render.go owns tab mapping; chat render.go and editor.go retain draft geometry.
5. <a id="source-5"></a> [View](../../internal/chatui/view.go#L74) and helpers in roster.go, preview.go, overlay.go, link_prompt.go and marquee.go.
6. <a id="source-6"></a> [Transition](../../internal/chatui/width.go#L8) — `setWidthMethod`; mouse.go cancels release of old gestures.
7. <a id="source-7"></a> [Quote maps](../../internal/chatui/quote.go#L56) — `quoteSource`, `rendered`; preview.go keeps preview eligibility/substitution; markdownview holds pure maps.
8. <a id="source-8"></a> [Runtime bounds](../../internal/chatui/runtime.go#L97-L102) — `runtimeText`, `runtimeRows` Note; attachments.go `imageUploaded`, staging.go `imageStaged`; boundaries in `internal/chatui/editor.go:L80-L94` — `clusters`.
9. <a id="source-9"></a> [Width regressions](../../internal/chatui/width_test.go#L15) — renderer cells, reports, symbol matrix, hits, cache transition and copy/Quote.
10. <a id="source-10"></a> [Clipping](../../internal/markdownview/width.go#L11) — `DisplaySequence`, `Truncate`, `TruncateLeft`; ultraviolet styled.go printString uses the same method decoder and ASCII correction.
