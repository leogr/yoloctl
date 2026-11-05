# Native chat Quote

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; Enter default at `4bc09137374e91d374ba6454cd8cef3d3da44240` and accompanying changes. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] Only the chat view's own drag highlight can be quoted: the view
requests all mouse motion, so yoloctl's local tab selection does not act over
it, and a host-terminal selection never reaches the view. While a highlight
lies inside one confirmed message, the footer leads with a `❝ Quote` chip,
also beside a release's copy notice, the highlight hints offer `Alt+Q quote`,
and a picked message holding the highlight lists Quote first, so Enter runs
it; a drag release or a highlight-clearing Esc returns Enter to the first
action (Reply without a highlight). A highlight in an unpicked message leaves
Enter to the composer. Alt+Q (repeats ignored), the chip or the action run it.
[1](#source-1) [2](#source-2) [3](#source-3) [4](#source-4) [9](#source-9)

[FACT] Quote inserts the highlighted part's original Markdown, or a rendered
table's selected cell text, at the cursor as one block: a line break first
unless the cursor starts a line, every line as `> line` (a blank source line as
`>`; a table row's TAB fields stay), then a blank line. It is one editor
insert, so one Ctrl+Z removes it. Only after the editor accepts it does Quote
set the reply target to that message (saying when it replaces another), clear
the highlight and focus the composer. It never calls the host clipboard, so
`seams.clipboard` and copy on select do not affect it; a release still copies
as before. [1](#source-1) [5](#source-5)

[FACT] Outside a rendered table the source is one contiguous slice of the
message's plain text. The line builders record, only when asked, where each
display byte comes from: verbatim bytes; units the view writes in place of
source (bullet, tab spaces, escaped character, a link's ` (url)`), which map
only whole; and dropped markup. Fence frames map only as whole lines; a start
on a fence opener includes the opener, an end on a closer the closer. Markup
touching either end is tried first, then the bare slice; edge line breaks are
dropped. A [reference preview](chat-references.md) shown in a line is one more
unit, standing for its token, and the highlighted text is compared in token
form, so the quote keeps the reference. A slice starting inside a fence gets
its opener, one ending inside gets its closer, or the opener's marker when the
source never closes it. The slice must render, in the current view, to exactly
the highlighted text. [1](#source-1) [6](#source-6)

[FACT] A highlight touching a recognized rendered table splits into runs of one
table's lines and of other lines, quoted in order without separators. Each
other run is such a slice; one that fails refuses all. On a side next to a
table, or reached to a line's start or end, it takes whole source lines, so
markers of lines that render empty and fenced blank lines stay; blank lines at
either end of the highlight go. A table run quotes no Markdown: one
line per logical row, its reached cells' displayed text within the highlight
joined by one TAB, an empty cell between them an empty field, whatever the
layout or wrapping. A row covered from its start or to its end reaches its
first or last cell (a whole row keeps empty edge fields); otherwise padding or
a separator at an end adds nothing; reached only there, a row adds no line.
Rule, spacers, markers and unselected headers are not copied; with nothing left
Quote refuses as no highlight. The notice says `Quoted selected table text`.
Source view, rejected tables and fences keep the slice route; Copy is
unchanged. [1](#source-1) [6](#source-6)

[FACT] Refusals change nothing and each has its own notice: no highlight,
several messages, an unconfirmed message, a file or image message outside
source view, an endpoint inside a unit or a slice that renders differently
(widen it or use source view), or a quote over the draft's byte or line limit.
Two lines of a table rejected for its size render as a table on their own, so
they are refused. [1](#source-1)

[FACT] Limits: the view's Markdown subset has no nested blocks, so a quoted
fence shows there as quoted text lines, and a quoted table's TABs display
expanded; cells split at unescaped pipes even inside backticks, and
Markdown-looking cell bytes may render once sent. Source view
maps every highlight except a cut tab. [6](#source-6)

[FACT] Fixture regressions cover the inserted block, reply, clipboard off,
links, emphasis, partial and refused cuts, source view, line markers, the
block's placement and one-step undo, fences inside, crossing, on frames and
unclosed, a rejected table, every refusal, the chip, hints, action and help.
Table regressions cover the examples, formatting cuts, cell edges,
endpoints, drags, mixed runs, layouts, code-span pipes, undo and limits. The mapping
returns the same lines as the renderer, and for eight short fixtures every pair
of grapheme endpoints, frame and rule rows included, in both views, quotes
source that renders to the highlight, refuses, or over a table matches cell
text derived independently from the highlight; a whole message always quotes.
They use fake callbacks, not a live room. [7](#source-7) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Quote](../../internal/chatui/quote.go#L11-L576) — `srcMap`, `lineSrc`, refusal notices, `quoteTarget`, `quotable`, `quoteLine`, `quoteSource`, `quoteSpans.blank`, `quoteSpans.exact`, `tableText`, `lineStart`, `lineEnd`, `fenced`, `rendered`, `quoteBlock`, `quoteSelection`, `insertQuote`.
2. <a id="source-2"></a> [Footer](../../internal/chatui/view.go#L645-L730) — `noticeRuns`, `quoteChip`, highlight hints.
3. <a id="source-3"></a> [Keys and actions](../../internal/chatui/model.go#L725-L1152) — `quoteKey`, `messageActions`, `runAction` (`quote`); `clickHit` in mouse.go.
4. <a id="source-4"></a> [Mouse mode](../../internal/chatui/view.go#L215) — all-motion mouse reporting; `selectionTarget` in internal/tui/selection.go (L53-L64) selects locally only when a live tab tracks no mouse.
5. <a id="source-5"></a> [Help](../../internal/chatui/overlay.go#L102-L111) — Messages and Selecting text rows.
6. <a id="source-6"></a> [Line builders](../../internal/chatui/body.go#L111-L224) — `displayLinesMap`; `tableLines` (L607-L732, cell ranges) and `tableCells`; `inlineMarkdownMap`, `listItem` in inline.go; `expandTabsMap` in render.go; `selectedParts` in selection.go.
7. <a id="source-7"></a> [Quote regressions](../../internal/chatui/quote_test.go#L1-L435) — nine tests, `TestQuoteInsertsTheSourceAndReplies` through `TestQuoteEveryHighlightIsExactOrRefused` with `tableOracle`; `TestTableSpacersCopyNothing` in table_spacing_test.go (L151-L219).
8. <a id="source-8"></a> [Table Quote regressions](../../internal/chatui/quote_table_test.go#L1-L363) — eight tests, `TestQuoteTableExamples` through `TestQuoteTableKeepsSourceBesideTables`.
9. <a id="source-9"></a> [Enter regressions](../../internal/chatui/quote_enter_test.go#L1) — Enter quotes from a pick (clipboard off too), Esc and ↑/↓ return it to Reply, Reply chip click; `escape` in model.go and `mouseUp` in mouse.go reset the action.
