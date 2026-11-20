# Native chat message display

Verification: `6e7df48d9682ee224e6a013066f45a710eae6f4b` and accompanying changes; re-read affected cited symbols when changing behavior.

[FACT] Chat uses the shared Markdown parser/layout through thin aliases and
wrappers; identity decoration, loaded previews, caches and host effects remain
in chat. [1](#source-1)

[FACT] Display text renders level-styled headings, quotes, bullets, number-aligned
ordered items (both kinds wrap under their text), `---` rules, fenced code and tables,
plus unpadded accent inline code, strong, italic, bold-italic, strikethrough and
links whose URL stays visible, and `<http(s)://…>` without brackets; unpaired markers, snake_case and escapes stay
literal. Tables need a matching, aligning delimiter row and end at a block
(fence, heading, quote, list); a pipe inside a code span stays in its cell; past 32 columns, 512 body rows or twice their
source plus 4 KiB once aligned, they stay text. Wider tables water-fill the
view (column floor 4 cells or widest grapheme) and wrap cells in place, or
wrap as prose if floors, that budget or the row limit forbid it. Prose wraps
at spaces, code per grapheme; rows keep their display line's byte range,
table rows their cell fragments. Decorative muted solid rules separate consecutive body rows in either theme,
including wrapped layouts. Their column dividers have a space on each side
(`─── │ ───`) rather than crosses; the header rule stays unchanged. Decorations
add no copied or quoted text. [5](#source-5) A table gets one empty row on each side whose
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
[2](#source-2) [6](#source-6) [7](#source-7) Bare URLs and `@mentions` are
click targets; own mentions stand out. Source mode shows the original; incoming
controls are inert. [2](#source-2) [3](#source-3) [4](#source-4)

[FACT] Chat layout uses its renderer-compatible width method throughout wrapping,
tables, tab expansion, composer geometry and chrome; supported mode-2027 reports
switch from wcwidth to grapheme widths. [8](#source-8)

## Sources

1. <a id="source-1"></a> [Shared renderer boundary](markdown-renderer.md).
2. <a id="source-2"></a> [Shared body rendering](../../internal/markdownview/body.go#L23) — `DisplayLine`, `parseLines`, `bodyRows`, `CodeCards`, `TableLines`, `FitTable`, `WaterFill`; chat delegates through markdown.go and decorates in body.go.
3. <a id="source-3"></a> [Inline Markdown](../../internal/markdownview/inline.go#L28) — `InlineMarkdown`, `InlineMarkdownMap`.
4. <a id="source-4"></a> [Rows and wrapping](../../internal/markdownview/rows.go#L21) — `Run`, `Paint`, `WrapRanges`, `HitRun`; chat transcript rows and anchors stay in chatui/rows.go.
5. <a id="source-5"></a> [Table row dividers](../../internal/markdownview/table_row_rules.go#L14) — `TableRowRule`; chat table_row_rules_test.go preserves rendering, gaps, copy and bounds assertions.
6. <a id="source-6"></a> [Code highlighting](../../internal/markdownview/highlight.go#L182) — `syntaxes`, `syntaxFor`, `tokenStyle`, `highlightLine`; shared body.go owns code-card/copy geometry.
7. <a id="source-7"></a> [Code-block regressions](../../internal/chatui/codeblock_test.go#L41) — exact copy, clipboard off, empty and narrow cards; shared markdownview/highlight_test.go tests lexer classes/state/fallback/tiling, chat highlight_test.go keeps the view integration.
8. <a id="source-8"></a> [Width methods and invalidation](chat-width.md).
