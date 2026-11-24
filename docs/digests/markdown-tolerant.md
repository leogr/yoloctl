# Tolerant Markdown rules

Verification: `e1f574bdf359d6c3f63c6bb573a7f3088bd8e294` and accompanying changes.

[FACT] Beside an open fence, the parser carries an open comment block, an open
indented code block and a list context. An open block owns its lines before
anything else is recognized; otherwise the first rule that matches applies:
comment opener, fence opener (up to 3 columns of indentation, any inside a
list), indented code start, table, setext heading, rule, `#` heading, quote,
list item, text. Columns expand leading TABs to the next multiple of 4.
Source mode applies none of these rules. [1](#source-1)

[FACT] Headings drop a closing run of unescaped `#` after a space or TAB
(`## T ##` shows `T`; `\#` escapes). A rule is up to 3 columns of indentation
then 3 or more of one of `-`, `*`, `_`, optionally spaced. Under paragraph text
a line of `=` or `-` is a setext underline: the text run becomes a level 1 or 2
heading and the underline a hidden line, so text followed by `---` is a heading.
Quotes accept `>` with or without a space and strip every nested level, one
two-cell bar each, as many bars as leave the text one cell. [1](#source-1) [2](#source-2)

[FACT] The list context stacks open items by source column: an item's content
column is its marker column and width plus the 1 to 4 spaces after it, or 1 for
more; it is kept apart from `ListItem`'s display offsets. Inside a list a
TAB-indented marker line is a nested item; outside one it is not a bullet.
Outside a list a line indented by 4 or more columns starts a code card where the
previous line is not paragraph or quote text, an indented fence or table there
included; it shows the lines without their first 4 columns, unwrapped and
unhighlighted, with a Copy button. An open code card checks forward once per
run of blank lines that needs lookahead. A blank line below four columns
belongs to the card if the next nonblank line has at least four columns.
Without an open card, parsing skips that lookahead. [1](#source-1) [3](#source-3) [7](#source-7)

[FACT] A small HTML subset, bare and case-insensitive: `<b>`/`<strong>`,
`<i>`/`<em>`, `<kbd>`/`<code>` pairs style their text, matched within the line
with a stack; `<details>`, `<summary>` and a one-line comment are hidden;
`<br>` ends a display line and starts another from the same source line. Other
tags, attributes and entities stay literal, as does a tag in a code span, after
a backslash or in an autolink. A line starting with `<!--` hides every line to
the first `-->` (from one backward pass); text after it is literal. In a table
cell `<br>` is one space with a forced break: such a table always goes through
the fit, a collapsed row shows its first line with …, labelled rows give each
segment its own line. A `<br>` inside a link shown by its label alone stays a
space, not a break: the label maps as one unit. [1](#source-1) [4](#source-4)
[5](#source-5)

[FACT] A hidden line keeps its place and `LineSource`, is `Decor` (never copied)
and gets no row. A `<br>` part has its own `LineSource`, the tag's bytes ending
the part before it. A dropped tag joins the character before an opening tag or
after a closing one, so a selection inside tagged text quotes it bare and a whole
line quotes its tags; the last line of a setext heading quotes its underline
when taken whole; indented code quotes its source lines with their indentation.
Quote of table rows is still the cells' displayed text (`a b` for `a<br>b`).
The exact-render check is unchanged, so a line that renders differently alone,
such as a TAB-indented line under a quote, is refused. [1](#source-1) [6](#source-6)

[FACT] Recorded deviations from GitHub, each showing more text, never less:
an unclosed `<!--` stays literal (D1); a comment opened in the middle of a line
must close on that line (D2); `<details>` collapses nothing and its content is
parsed like any line (D3). Still unsupported: lazy continuation of quotes or list
paragraphs, blocks inside a quote, blocks inside a list item beyond the existing
display, reference links and entities. [1](#source-1)

[FACT] When an inline link opener finds no `]` followed by `(` in its suffix,
`InlineMarkdownMap` skips that search for subsequent openers on the same line.
`scanTags` similarly stops looking for `-->` after a miss. Each search still uses
the nearest delimiter when one exists. Verified at `5b64129` and accompanying
changes. [2](#source-2) [4](#source-4)

[FACT] Tests: `tolerant_test.go` covers each rule, the corpus cases in
`testdata/tolerant` (reviewed after-text), Copy and Quote mapping and
`FuzzTolerantMarkdown`, whose seeds run in every test run (no panic, one source
per line, rows within width, source mode one line per source line; texts with a
Prepend character or a spacing mark skipped for the renderer's documented
grapheme-width limit). [6](#source-6)

## Sources

1. <a id="source-1"></a> [body.go](../../internal/markdownview/body.go#L156) — `parseLines`, `withoutClosingHashes`, `thematicBreak`, `setextUnderline`, `quoteMarkers`, `tolerantListItem`, `indentedCodeLine`, `fenceIndentOK`, `commentBlockEnd`, `commentLine`, `splitAtBreaks`.
2. <a id="source-2"></a> [inline.go](../../internal/markdownview/inline.go#L64) — escapes (`#`, `<` added).
3. <a id="source-3"></a> [blocks.go](../../internal/markdownview/blocks.go#L1) — `indentCols`, `listMarker`, `listContext`.
4. <a id="source-4"></a> [html_inline.go](../../internal/markdownview/html_inline.go#L1) — `stripTags`, `scanTags`, `tagAt`, `styleTags`.
5. <a id="source-5"></a> [table cells](../../internal/markdownview/body.go#L1239) — `TableLines`, `tableCell`, `segments`, `cellBreaks`, `fitTableColumns`; table_compact.go `rowClipped`, `compactRecord`; table_fallback.go `labelledTable`; `TestTableCellLineBreakInLinkLabel`, verified at `61397bf` and accompanying changes.
6. <a id="source-6"></a> [tolerant_test.go](../../internal/markdownview/tolerant_test.go#L1) — rule, corpus, mapping and fuzz tests; chatui `TestSetextHeadingInChat`, `TestRuleInChat`, markdown_parity_test.go.

7. <a id="source-7"></a> [Indented code blank runs](../../internal/markdownview/body.go#L243) — `parseLines` and `TestIndentedCodeBlankRuns`, verified at `4624036` and accompanying changes.
