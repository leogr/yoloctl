# Shared Markdown renderer

Verification: `3bae6dd1d6ffa1356f7678850584fe6d9cf0fac8` and accompanying changes.

[FACT] `internal/markdownview` owns the existing Markdown subset's pure parser,
lexer, logical lines, layout, source maps and copy/hit geometry. `Parse` takes
text, source mode, theme, `ansi.Method` and an optional synchronous decorator;
`Layout` produces styled rows with logical byte ranges, table fragments and
an explicit display-limit flag. `Paint` converts those runs to terminal text.
The package owns no model, transport, file reader, cache, clipboard or opener.
Styles and themes reuse `spaceview`; no Markdown dependency is added.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Parsing sanitizes invalid UTF-8 and controls, normalizes line endings
and expands tabs with the chosen width method. Source mode and the existing
block/inline/lexer behavior share that implementation. Tables retain 32-column,
512-body-row and aligned-text budgets; layout keeps the 16,384-row bound and
its existing notice. Code-card copy intents retain block source bytes. These
are the existing subset and limits, not a CommonMark contract.
[2](#source-2) [4](#source-4) [5](#source-5)

[FACT] Within that subset: a table pipe inside a code span on its line stays in
its cell (`tableSeparators` pairs backtick runs as inline code does, in one
pass with a binary search per run); headings stay bold at cell size, level 1 in
the accent, 2 in body text, 3 in the label colour, 4–6 in the accent. Ordered
items keep their written numbers and delimiter; decorative padding right-aligns
the numbers of a run of same-indentation items (deeper items continue it, other
lines end it). Ordered and bullet items wrap under their text unless that column
would be under 8 cells. A trimmed `---` line is a decorative rule as wide as
the text; table delimiters win. `<http(s)://…>` shows without brackets as a
URL intent that decorators never re-style. Padding, gutters and rules are
never copied; source maps drop the brackets as markup.
[2](#source-2) [5](#source-5) [14](#source-14)

[FACT] Ordinary `Parse` omits per-byte source maps. `MapSource` explicitly
requests `LineSource` mappings into sanitized source, including dropped markup
and whole replacement units. `SourceSelection.Exact` tries the same bounded
source/fence reconstruction and re-render comparison; `TableText` returns
selected table cell text. Callers provide the matching pure renderer and
retain selection eligibility, refusal messages and insertion effects.
[6](#source-6) [7](#source-7)

[FACT] Shared URL and code-copy regions are inert intents. Other action kinds
and payload strings are opaque caller data. With no decorator, mentions and
message references remain ordinary text. Chat supplies its existing token
recognition/style ordering through a decorator and thin adapters. Identity
resolution, loaded-preview lookup, table-preview exclusion, cache invalidation,
message selection, Quote/reply/undo and host effects stay in chat.
[1](#source-1) [8](#source-8) [9](#source-9)

[FACT] `ApplyReplacements` updates text, spans and original-copy ranges for
ordered, nonoverlapping replacements whose endpoints align to piece boundaries.
Chat chooses those ranges and labels. `ReplacementBounds` widens hits to whole
replacements; `ReplacementSourceMap` composes with explicit source maps, so
whole previews copy/quote their original token and partial ones stay inexact.
[10](#source-10) [11](#source-11)

[FACT] Frozen chat fixtures, updated only by intended rendering changes, cover 192 combinations of eight content domains,
widths 20/80/120, both themes, both width methods and source/rendered mode:
styles, rows, painted text, hit/action ranges, copy and source-selection results.
Plain-consumer tests exercise the same package without chat resolution, lazy
maps, opaque replacements and the existing row limit. These tests establish
component parity, not native terminal, live-room or future file-view behavior.
[12](#source-12) [13](#source-13)

## Sources

1. <a id="source-1"></a> [Public document API](../../internal/markdownview/document.go#L1) — `ParseOptions`, `Parse`, `Layout`, `RenderText`, intents and default URL decoration.
2. <a id="source-2"></a> [Logical lines and layout](../../internal/markdownview/body.go#L11) — `DisplayLine`, `parseLines`, `bodyRows`, `TableLines`, limits and code cards.
3. <a id="source-3"></a> [Rows and painting](../../internal/markdownview/rows.go#L10) — `Run`, `Paint`, `WrapRanges`, `HitRun`, `ActionAt`.
4. <a id="source-4"></a> [Sanitization and tabs](../../internal/markdownview/render.go#L10) — `Plain`, `ExpandTabsMap`, fence helpers; width.go owns selected-method clipping.
5. <a id="source-5"></a> [Inline parsing](../../internal/markdownview/inline.go#L28) — `InlineMarkdownMap`; highlight.go holds the existing lexer.
6. <a id="source-6"></a> [Source mapping](../../internal/markdownview/quote.go#L10) — `SourceMap`, `LineSource`, `TableText`, `LineStart`, `LineEnd`, `Fenced`.
7. <a id="source-7"></a> [Source selection](../../internal/markdownview/selection.go#L7) — `SourceSelection.Blank`, `Exact`.
8. <a id="source-8"></a> [Chat adapters](../../internal/chatui/markdown.go#L10) — aliases, `chatDecorator`, `displayLinesMap`, `bodyRows`.
9. <a id="source-9"></a> [Chat Quote effects](../../internal/chatui/quote.go#L25) — eligibility, source assembly, `quoteSpans`, `quoteSelection`, `insertQuote`; body.go keeps token identity recognition and preview.go keeps preview lookup/cache decisions.
10. <a id="source-10"></a> [Generic replacements](../../internal/markdownview/replacement.go#L9) — `Replacement`, `ApplyReplacements`.
11. <a id="source-11"></a> [Replacement geometry](../../internal/markdownview/preview.go#L5) — `TextSub`, `ReplacementBounds`, `ReplacementSourceMap`.
12. <a id="source-12"></a> [Frozen chat parity](../../internal/chatui/markdown_parity_test.go#L1) — `TestMarkdownFrozenParity`, testdata/markdown-parity.json; no regeneration path.
13. <a id="source-13"></a> [Plain-consumer regressions](../../internal/markdownview/document_test.go#L15) — generic render/layout, explicit maps, replacement copy/refusal and limit metadata; highlight_test.go retains the lexer corpus.
14. <a id="source-14"></a> [Subset additions](../../internal/markdownview/improvements_test.go#L64-L320) — list alignment/wrapping/coverage, rule, autolinks, headings, separators against a naive reference; code in body.go `parseLines` (L108), `HeadingColor`, `listRanges` (L435), `tableSeparators` (L903) and inline.go `autolinkEnd` (L152), `listItem`, `alignOrdered` (L229-L300).
