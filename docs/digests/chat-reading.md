# Native chat reading and selection

Verification: `d9aec4ac854ffd5b4f60a93817b8c5afce10847d` and accompanying changes.

[FACT] Buttonless motion only highlights ([hover](chat-hover-paste.md)); a click
is press and release on one target. A left drag selects logical points (message,
display line, grapheme bytes), surviving scroll, resize and the panel but not
endpoint eviction. A late width-method change instead clears highlights and
cancels the press without acting on its release; message/line reading anchors,
drafts and replies remain. [10](#source-10) Release copies once via host `Copy` unless copy on select is
off; `seams.clipboard` off writes nothing; over 1 MiB is refused. Past an edge a
drag scrolls a row per 40 ms, loading no history. [3](#source-3) [4](#source-4)

[FACT] Drag copy is the displayed body text, except that a [reference
preview](chat-references.md) copies as its reference: soft wraps rejoin, lines join with
`\n`, messages with a blank line. Names, times, labels, frames, table rules,
notices and the panel are never copied; a half-covered wide grapheme is. Tabs
copy as shown spaces; Alt+Y copies original bytes. A table wrapped to the view
copies its aligned logical rows, cells in order with separators, so a drag
across cells may highlight parts of several screen rows; padding and separators
snap to adjacent cell text. [3](#source-3) [5](#source-5)

[FACT] The wheel moves one row per event, like TUI scrollback on high-resolution
hosts; PgUp/PgDn move a screen minus two rows, Ctrl+↑/↓ one; scroll-only events
reuse built rows for unchanged content and width. Scrolled back, the divider
centers `↓ Jump to latest` (`↓ N new · …`), shortening label then room name
before giving way. Click, Alt+End and Esc jump alike, keeping draft, reply,
images and selection; Esc first clears overlays, suggestions, a highlight or
message focus, and at the tail cancels a reply, then the selection. Arrivals
keep the reading position; only the jump, sending or scrolling to the end
returns to the live tail. [1](#source-1)
[2](#source-2) [6](#source-6) [7](#source-7) [8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [View layout](../../internal/chatui/view.go#L75) — `View`, `place`, `topBar`, `composerDivider`, `composerStatus`, `inputRow`, `statusRow`, `footer`.
2. <a id="source-2"></a> [Keys and actions](../../internal/chatui/model.go#L736) — `key`, `escape`, `runAction`, `append`, `pick`, `jumpToLatest`.
3. <a id="source-3"></a> [Selection](../../internal/chatui/selection.go#L14) — `selPoint`, `selectedText`, `pointAt`, `copySelection`.
4. <a id="source-4"></a> [Mouse routing](../../internal/chatui/mouse.go#L15) — `mouseDown`, `mouseMove`, `dragStep`, `mouseUp`.
5. <a id="source-5"></a> [Shared copy text](../../internal/markdownview/body.go#L67) — `DisplayLine.CopyText`; shared rows.go owns `HitRun`; chat selection.go owns message/gesture routing.
6. <a id="source-6"></a> [TUI wheel](../../internal/tui/mouse.go#L284-L308).
7. <a id="source-7"></a> [Interaction regressions](../../internal/chatui/polish_test.go#L1) — selection, attach, reconnect, presence, scroll.
8. <a id="source-8"></a> [Reading regressions](../../internal/chatui/reading_test.go#L25-L79) — jump control.
9. <a id="source-9"></a> [UI regressions](../../internal/chatui/ui_package_test.go#L71) — actions, jump, padding, lookups.
10. <a id="source-10"></a> [Width transition and fresh hit coordinates](chat-width.md).
