# Terminal log wrapping

Verification: `351da19df8eb0c537435095db835d13678bc5372` and accompanying changes.

[FACT] Pretty log text can use an optional `Options.TerminalWidth` callback.
The host CLI stderr and standalone follow-log adapters supply it: each rendered
record or repeat summary checks that the original destination is still a
terminal, then reads its current column count with `term.GetSize`. No pixel,
row or cell geometry is inferred. The public renderer itself reads no terminal.
[1](#source-1) [3](#source-3)

[FACT] A pretty record's label/glyph/time prefix remains on its first line.
Message, fields, duration and error text word-wrap into the remaining columns;
continuations have spaces to the same detail column. Executor detail and
repeat-summary text use their own existing columns. The pinned ANSI library's
`Wrap` preserves styling and uses the same grapheme-cell widths as the existing
`StringWidth`; an overlong word can split between graphemes. Explicit message
newlines still start aligned continuations. [2](#source-2)

[FACT] Nil, failed or nonpositive width readings leave bytes unchanged. When
the prefix already fills the terminal, wrapping is disabled rather than
changing its column. A grapheme wider than the remaining columns is indivisible.
Plain/file output, framed wire records and command data/JSON writers are
unchanged. The live step block retains its separate row truncation.
[1](#source-1) [2](#source-2) [4](#source-4) [5](#source-5)

[FACT] Focused controls cover resize between writes, Unicode/combining/emoji
text and ANSI colour, explicit/error lines, detail/repeat columns, unknown or
narrow widths, plain/wire byte parity, and actual PTY column changes and closed
or non-terminal descriptors. They do not qualify every native terminal.
[6](#source-6) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Host terminal adapter](../../internal/logging/logging.go#L123-L270) — `Init`, `NewFollowLogger`, `terminalWidth`, `IsTerminal`.
2. <a id="source-2"></a> [Pretty layout](../../pkg/prettylog/format.go#L257-L420) — `terminalWidth`, `prettyText`, `render`, `summary`.
3. <a id="source-3"></a> [Width option](../../pkg/prettylog/logger.go#L117) — `Options.TerminalWidth`.
4. <a id="source-4"></a> [Wire/filter boundary](../../pkg/prettylog/logger.go#L294) — `emitOwned`.
5. <a id="source-5"></a> [Step block](../../pkg/prettylog/stepview/stepview.go#L251-L261) — terminal-row truncation.
6. <a id="source-6"></a> [Renderer controls](../../pkg/prettylog/wrap_test.go#L1) — hanging indent, resize, Unicode/colour and byte parity.
7. <a id="source-7"></a> [PTY adapter controls](../../internal/logging/terminal_width_test.go#L1) — live column probes and non-terminal opt-out.
