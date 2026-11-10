# Native chat Quote

Verification: `3bae6dd1d6ffa1356f7678850584fe6d9cf0fac8` and accompanying changes.

[FACT] Only the chat view's own drag highlight can be quoted: the view
requests all mouse motion, so yoloctl's local tab selection does not act over
it and a host-terminal selection never reaches it. While a highlight
lies inside one confirmed message, the footer leads with a `❝ Quote` chip (also
beside a release's copy notice) and the hints offer `Alt+Q quote`. A picked
message holding the highlight lists Quote first, so Enter defaults to Quote
while message actions have focus (arrows or a chip still choose another); a
drag release or a highlight-clearing Esc returns Enter to the first action.
A highlight in an unpicked message leaves Enter to the composer. Alt+Q
(repeats ignored), the chip or the action run it.
[1](#source-1) [2](#source-2) [3](#source-3) [4](#source-4) [9](#source-9)

[FACT] Quote inserts the highlighted part's original Markdown, or a rendered
table's selected cell text, at the cursor as one editor insert (one Ctrl+Z):
a line break unless the cursor starts a line, each line as `> line` (a blank
source line as `>`; table TAB fields stay), then a blank line. Only after the
editor accepts it does Quote set the reply target (saying when it replaces
another), clear the highlight and focus the composer. It never calls the host
clipboard, so `seams.clipboard` and copy on select do not affect it; a release
still copies. [1](#source-1) [5](#source-5)

[FACT] Outside a rendered table the source is one contiguous slice of the
message's plain text. Line builders record, on request, where each display
byte comes from: verbatim bytes; units the view writes in place of source
(bullet, tab spaces, escaped character, a link's ` (url)`), mapped only whole;
and dropped markup. Fence frames map only as whole lines (an opener start
includes the opener, a closer end the closer). Markup at either end is tried
first, then the bare slice; edge line breaks are dropped. A
[reference preview](chat-references.md) is one more unit standing for its
token, compared in token form, so the quote keeps the reference. A slice
starting inside a fence gets its opener, one ending inside its closer, or the
opener's marker when the source never closes it. The slice must render, in the
current view, to exactly the highlighted text. [1](#source-1) [6](#source-6)

[FACT] A highlight touching a recognized rendered table splits into runs of one
table's lines and of other lines, quoted in order without separators. Each
other run is such a slice; one failure refuses all. Next to a table, or reached
to a line's start or end, it takes whole source lines, so markers of lines that
render empty and fenced blank lines stay; blank lines at either end go. A table
run quotes no Markdown: per logical row, one line of its reached cells' displayed
text within the highlight, joined by TAB, an empty cell between them an empty
field, in any layout or wrapping. A row covered from its start or to its
end reaches its first or last cell (a whole row keeps empty edge fields);
padding or a separator at an end adds nothing, and a row reached only there
adds no line. Rule, spacers, markers and unselected headers are not copied;
with nothing left Quote refuses as no highlight; the notice is `Quoted
selected table text`. Source view, rejected tables and fences keep the slice
route; Copy is unchanged. [1](#source-1) [6](#source-6)

[FACT] Refusals change nothing, each with its own notice: no highlight,
several messages, an unconfirmed message, a file or image message outside
source view, an endpoint inside a unit or a slice that renders differently
(widen it or use source view), or a quote over the draft's byte or line limit.
Two lines of a size-rejected table render as a table on their own, so they are
refused. [1](#source-1)

[FACT] Limits: the view's Markdown subset has no nested blocks, so a quoted
fence shows as quoted text lines and a quoted table's TABs display expanded;
cells split at unescaped pipes outside a matched code span, and Markdown-looking cell
bytes may render once sent. Source view maps every highlight except a cut tab.
[6](#source-6)

[FACT] Regressions cover the block, reply, clipboard off, links, emphasis,
cuts, source view, markers, placement, undo, fences, a rejected table, every
refusal, the chip, hints, action, help and Enter; table ones cover examples,
cuts, cell edges, endpoints, drags, mixed runs, layouts, code-span pipes, undo
and limits. The mapping returns the renderer's lines, and for eight
short fixtures every pair of grapheme endpoints, frame and rule rows included,
in both views, quotes source rendering to the highlight, refuses, or over a
table matches independently derived cell text; a whole message always quotes.
They use fake callbacks, not a live room. [7](#source-7) [8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [Chat Quote](../../internal/chatui/quote.go#L12) — refusal notices, `quoteTarget`, `quoteSource`, `quoteSpans`, `quoteBlock`, `quoteSelection`, `insertQuote`; pure mapping delegates to markdownview.
2. <a id="source-2"></a> [Footer](../../internal/chatui/view.go#L645-L730) — `noticeRuns`, `quoteChip`, highlight hints.
3. <a id="source-3"></a> [Keys and actions](../../internal/chatui/model.go#L725-L1152) — `quoteKey`, `messageActions`, `runAction` (`quote`); `clickHit` in mouse.go.
4. <a id="source-4"></a> [Mouse mode](../../internal/chatui/view.go#L215) — all-motion reporting; `selectionTarget` (internal/tui/selection.go L53-L64) selects locally only when a live tab tracks no mouse.
5. <a id="source-5"></a> [Help](../../internal/chatui/overlay.go#L109-L124) — Messages and Selecting text rows.
6. <a id="source-6"></a> [Shared line builders](../../internal/markdownview/body.go#L101) — `parseLines`, `TableLines`, `TableCells`; shared inline.go owns `InlineMarkdownMap`, quote.go owns `SourceMap`, `TableText` and fences, selection.go owns `SourceSelection.Exact`; chat selection.go retains `selectedParts`.
7. <a id="source-7"></a> [Quote regressions](../../internal/chatui/quote_test.go#L1-L435) — nine tests, `TestQuoteInsertsTheSourceAndReplies` through `TestQuoteEveryHighlightIsExactOrRefused`; `TestTableSpacersCopyNothing` in table_spacing_test.go (L151-L219).
8. <a id="source-8"></a> [Table Quote regressions](../../internal/chatui/quote_table_test.go#L1-L383) — eight table tests; markdown_improvements_test.go covers list drag/Quote, autolink and rule Quote.
9. <a id="source-9"></a> [Enter regressions](../../internal/chatui/quote_enter_test.go#L1-L166) — five Enter tests; `escape` (model.go L934) and `mouseUp` (mouse.go L179) reset the action.
