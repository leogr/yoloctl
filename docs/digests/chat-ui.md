# Native chat UI component

Verification: `d9aec4ac854ffd5b4f60a93817b8c5afce10847d` and accompanying changes.

[FACT] `internal/chatui` is a transport-neutral Bubble Tea model. Its caller
supplies connection events, logical messages, correlated receipts, roster/topic
snapshots and send/copy/link/attach/nickname/reconnect callbacks. It opens no
connection, reads no credentials and owns no server lifecycle; it starts
disconnected. `Outgoing.ID` correlates Pending, Confirmed, Rejected and
Uncertain states; queue acceptance is not confirmation. [1](#source-1)

[FACT] Enter submits once; Alt+Enter, Shift+Enter or Ctrl+J insert newlines.
Bracketed paste never submits; committed text cannot impersonate shortcuts. The
editor keeps newlines, tabs and indentation, normalizes CRLF/CR to LF, rejects
invalid UTF-8 and strips other controls. Movement and deletion use pinned ANSI
grapheme boundaries. Ctrl+A/E/K/U/W/Y edit lines, kill and yank;
Ctrl+Z/Ctrl+Shift+Z undo and redo. Vertical movement shares the rendered wrapped
layout. Drafts default to 64 KiB (at most 1 MiB, 8192 lines); oversized edits
fail atomically. Undo keeps at most 64 snapshots/4 MiB. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] A draft remains editable while pending. Confirmation clears it only if
its text and editor/reply revisions still match the submitted snapshot.
Rejection preserves it; uncertainty or disconnect prevents another send until an
explicit receipt resolves the outcome. The component never retries or
reconnects. A correlated full echo must also match the original text. Replay
overlap is deduplicated by message ID without counting as unread. When
disconnected, reconnecting, unjoined, deleted or resolving actions, empty draft
placeholders and the default footer truthfully display blocking status and draft
retention rather than advertising send, while local slash commands remain
executable. [1](#source-1) [15](#source-15)

[FACT] Chat uses the shared Markdown parser/layout through thin aliases and
wrappers; identity decoration, loaded previews, caches and host effects remain
in chat. [21](#source-21)

[FACT] Display text renders headings, quotes, bullets, fenced code and tables,
plus unpadded accent inline code, strong, italic, bold-italic, strikethrough and
links whose URL stays visible; unpaired markers, snake_case and escapes stay
literal. Tables need a matching, aligning delimiter row and end at a block
(fence, heading, quote, list); past 32 columns, 512 body rows or twice their
source plus 4 KiB once aligned, they stay text. Wider tables water-fill the
view (column floor 4 cells or widest grapheme) and wrap cells in place, or
wrap as prose if floors, that budget or the row limit forbid it. Prose wraps
at spaces, code per grapheme; rows keep their display line's byte range,
table rows their cell fragments. Decorative muted solid rules separate consecutive body rows in either theme,
including wrapped layouts. Their column dividers have a space on each side
(`─── │ ───`) rather than crosses; the header rule stays unchanged. Decorations
add no copied or quoted text. [18](#source-18) A table gets one empty row on each side whose
neighbouring source line is not blank (empty or whitespace-only), never at the
message's edges, once whatever its layout; blank lines stay as written. These
spacers copy nothing, a press on one starts at the line beyond it, and every
row, spacer or frame, counts toward the 16,384-row limit.
Fenced code is a `Popup`-tinted card sized to its widest line or label plus a
padding cell per side: a dim label row, the code, a padding row. A non-empty
block's label row ends in a `copy` button (the label truncates first; a card
narrower than the button omits it) that copies the block's source lines.
A built-in lexer colors keywords, strings, comments, numbers, types and keys
for common languages named by the fence's first word (Go, Python, JS/TS, Rust,
C/C++, Java, C#, Kotlin, Swift, Ruby, PHP, Lua, shells, SQL, Dockerfile, Make,
JSON, YAML, TOML, diff), carrying block comments and multi-line strings across
lines; it only styles bytes, and other languages stay plain.
[4](#source-4) [19](#source-19) [20](#source-20) Bare URLs and `@mentions` are
click targets; own mentions stand out. Source mode shows the original; incoming
controls are inert. [4](#source-4) [5](#source-5) [6](#source-6)

[FACT] Chat layout uses its renderer-compatible width method throughout wrapping,
tables, tab expansion, composer geometry and chrome; supported mode-2027 reports
switch from wcwidth to grapheme widths. [16](#source-16)

[FACT] Trusted participant labels use the caller's saved room nickname
(`Identity.Nick`), falling back to the live nick; canonical identity names are
not displayed. Human role, box membership and agent also come from that
account-keyed identity map. Known nicknames carry no mark; an unknown account shows
only its dim plain nick with no glyph or qualifier label (in cards and the
Participants panel too), and source mode shows raw nick(account). Delivery state is a
separate label. Times are local; replies show author and snippet; the human gets
"you". Agent glyphs and colours come from the TUI `agent_icons`; humans use the
two-cell 👤 emoji in its own colors. Glyphs get a two-cell slot plus a gap, like
tab icons. [7](#source-7) [17](#source-17)

[FACT] Native chat applies host `seams.browser`: `on` opens on Ctrl+O or a link
click; `off` disables opening; `ask` and unknown values require another
non-repeat Ctrl+O in a local prompt displaying the fixed destination. Esc/Ctrl+C
cancels; text, paste and Enter neither approve nor edit the obscured draft. URLs
are revalidated before opening. [8](#source-8) [9](#source-9)

[FACT] PgUp, Ctrl+↑ or the wheel at the top calls `LoadOlder` with the oldest
ID. Older messages insert in order without unread; one page is outstanding, none
while disconnected; an empty page ends paging with a top marker. Past display
bounds a page's oldest messages give way, keeping history contiguous. Display
eviction past 256 messages/2 MiB/32768 logical lines is visible and keeps
unresolved correlation. A body past 16384 rows shows a limit notice; its
original stays copyable. [1](#source-1) [10](#source-10)

[FACT] The `chatstream` adapter reports the v2 `ready` limits, then requests
history, NAMES and WHO. The roster follows NAMES, JOIN/PART/QUIT/KICK/NICK and
op/voice MODE; WHO rows and AWAY events add authenticated accounts and away
state. RPL_TOPIC/NOTOPIC/TOPICWHOTIME and live TOPIC for the room become bounded
topic events. History requests resolve in order by batch or FAIL CHATHISTORY;
after a read gap, paging stays off and late history is discarded.
[11](#source-11) [12](#source-12)

[FACT] Logical line/offset anchors preserve reading position across resize, the
participants panel, arrivals and eviction; in a wrapped table the row holding
the anchor's cell byte wins, else the row showing the next cell text.
Disconnects advise rerunning
`yoloctl chat` and flag an uncertain last send; the only exception is an
explicit reconnect (interaction digest). [1](#source-1) [10](#source-10)

[FACT] Component regressions cover paste, grapheme/kill editing, receipts,
replay, callbacks, Unicode bounds, Markdown, code cards, source mode, body
limits and anchors, with fake callbacks, not AIRC, a VM or a graphical terminal;
host key consumption, IME and CLI/stream integration are outside that evidence.
An opt-in contract test runs the real AIRC JSONL v2 client and server through
the adapter from `YOLOCTL_TEST_AIRC_BIN_DIR`. [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [Model and integration types](../../internal/chatui/model.go#L34) — messages, `Options`, `Update`, `submit`, `delivery`, `incoming`, `append`, `evict`.
2. <a id="source-2"></a> [Editor](../../internal/chatui/editor.go#L12) — bounds, `cleanInput`, grapheme editing and undo.
3. <a id="source-3"></a> [Draft layout](../../internal/chatui/render.go#L111) — `draftLayout`, shared by cursor and composer.
4. <a id="source-4"></a> [Shared body rendering](../../internal/markdownview/body.go#L23) — `DisplayLine`, `parseLines`, `bodyRows`, `CodeCards`, `TableLines`, `FitTable`, `WaterFill`; chat delegates through markdown.go and decorates in body.go.
5. <a id="source-5"></a> [Inline Markdown](../../internal/markdownview/inline.go#L28) — `InlineMarkdown`, `InlineMarkdownMap`.
6. <a id="source-6"></a> [Rows and wrapping](../../internal/markdownview/rows.go#L21) — `Run`, `Paint`, `WrapRanges`, `HitRun`; chat transcript rows and anchors stay in chatui/rows.go.
7. <a id="source-7"></a> [Transcript](../../internal/chatui/transcript.go#L32-L299) — `humanGlyph`, `identify`, `transcript`, `header`; `iconSlot` in hover.go.
8. <a id="source-8"></a> [Browser confirmation](../../internal/chatui/link_prompt.go#L18) — fixed destination and modal input.
9. <a id="source-9"></a> [Host browser policy](../../cmd/yoloctl/chat_view.go#L730-L748) — `configureChatBrowser`, validation and cancellation.
10. <a id="source-10"></a> [Scrolling and anchors](../../internal/chatui/render.go#L238) — `moveScroll`, `captureAnchor`; `place` in view.go.
11. <a id="source-11"></a> [Stream adapter](../../internal/chatstream/client.go#L156) — `Run`, `topicEvent`, `rosterEvent`, `record`, `historyGap`.
12. <a id="source-12"></a> [Roster, paging and topic regressions](../../internal/chatstream/roster_test.go#L9) — counts, refusals, gaps, `TestTopicEvents`.
13. <a id="source-13"></a> [Component regressions](../../internal/chatui/model_test.go#L15) — with `render_test.go`, `review_test.go`, `client_ux_test.go`, `link_prompt_test.go`, `markdown_test.go`, `reading_test.go` (code cards), `table_wrap_test.go` (wrapped tables), `table_spacing_test.go` (table spacers), `send_readiness_test.go`.
14. <a id="source-14"></a> [Real AIRC contract test](../../internal/chatstream/airc_integration_test.go#L1) — opt-in end-to-end adapter test.
15. <a id="source-15"></a> [Composer readiness](../../internal/chatui/view.go#L534-L742) — `composerStatus`, `inputRow`, `footer`.
16. <a id="source-16"></a> [Width methods and invalidation](chat-width.md).
17. <a id="source-17"></a> [Nickname display regressions](../../internal/chatui/nickname_display_test.go#L40) — `TestParticipantDisplaysUseSavedNicknames`, `TestNicknameCardsKeepSavedNickAndConnectionAlias`, `TestNicknameFallbackAndRawSourceIdentity`; generated labels, aliases, source and untrusted sessions.

18. <a id="source-18"></a> [Table row dividers](../../internal/markdownview/table_row_rules.go#L14) — `TableRowRule`; chat table_row_rules_test.go preserves rendering, gaps, copy and bounds assertions.
19. <a id="source-19"></a> [Code highlighting](../../internal/markdownview/highlight.go#L182) — `syntaxes`, `syntaxFor`, `tokenStyle`, `highlightLine`; shared body.go owns code-card/copy geometry.
20. <a id="source-20"></a> [Code-block regressions](../../internal/chatui/codeblock_test.go#L41) — exact copy, clipboard off, empty and narrow cards; shared markdownview/highlight_test.go tests lexer classes/state/fallback/tiling, chat highlight_test.go keeps the view integration.

21. <a id="source-21"></a> [Shared renderer boundary](markdown-renderer.md).
