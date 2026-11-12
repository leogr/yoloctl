# Chat image previews (host boundary)

Verification: `484b52b6eb83b3bc03f30406d28110f262fb5179` and accompanying changes.

[FACT] Previews use Kitty Unicode placeholder cells: U+10EEEE with a row and a
column diacritic, drawn in a 24-bit foreground colour that is the image ID.
`chatimage.Registry` records, per owning session (compared by identity), the
one placement it may display: ID, rows and columns. Activating a placement
replaces the session's earlier one and records its chip in the copy ledger;
release forgets it. A replaced or reopened session is a new owner and inherits
nothing. [1](#source-1)

[FACT] Every frame the TUI composes passes `chatimage.FilterLine`: a
placeholder cell stays only if it has exactly a row and a column mark, both
inside the owner's registered size, and its colour is that placement's ID.
Any other placeholder cell, including one forged by another pane or a stale
session, and every placeholder in the palette console, becomes one space, so
row width is kept. Well-formed escape sequences and other text are kept byte
for byte; the scanner is strict, so a bare ESC, an unfinished CSI or an
unterminated OSC never hides a cell. Rows without placeholders are not copied.
Workload-controlled chrome text, a session's title wherever it is drawn and a
link prompt's URL, passes `NeutralizeText`, which also turns each placeholder
cell into a space. [1](#source-1) [2](#source-2)
[3](#source-3)

[FACT] IDs route cells to images; they are not secrets. The filter changes
presentation only: native cells, history and copy keep the original
characters (copy maps them to chips, see the terminal selection digest).
[1](#source-1) [2](#source-2)

## Sources

1. <a id="source-1"></a> [filter.go](../../internal/chatimage/filter.go#L1-L212) — `Placement`, `Registry`, `diacriticIndex`, `FilterLine`, `escapeLen`, `NeutralizeText`; tests in filter_test.go.
2. <a id="source-2"></a> [frames.go](../../internal/tui/frames.go#L40-L83) — `consoleDisplayFrame`, `sessionFrame`, `filterImages`; `View` captures in tui.go.
3. <a id="source-3"></a> [image_filter_test.go](../../internal/tui/image_filter_test.go#L1) — owner, forging pane, console, composed view, release and a placeholder title; `emulator.Title` and the browser popup URL use `NeutralizeText`.
