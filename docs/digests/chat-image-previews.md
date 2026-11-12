# Chat image previews (host boundary)

Verification: `484b52b6eb83b3bc03f30406d28110f262fb5179` and accompanying changes; image channel on `b96ffc26c67ed63006d0dfa55a1b9c228d8e18ba` and accompanying changes.

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

[FACT] A session started with `term.Options.OnImage` gets a private
socket-pair channel named by `YOLOCTL_TUI_IMAGE_FD`; other sessions get neither
descriptor nor variable, which is removed even when the TUI's own environment
has it. Frames are a 4-byte length plus strict JSON of at most
16 KiB, version 1, with a closed set of operations: the host sends capability,
ready and error; the child sends preview (validated room, disk and human
identities, channel, message, artifact UUID, hash and 1 B–4 MiB size) and
release (a host-issued token). Fields of another operation, unknown keys,
trailing data or the wrong direction close the channel; the session itself
continues, and closing it never joins a blocked handler. The child claims the
descriptor once and removes the variable. Frames carry no image bytes, paths,
URLs, argv or terminal sequences. [4](#source-4) [5](#source-5)

[FACT] IDs route cells to images; they are not secrets. The filter changes
presentation only: native cells, history and copy keep the original
characters (copy maps them to chips, see the terminal selection digest).
[1](#source-1) [2](#source-2)

## Sources

1. <a id="source-1"></a> [filter.go](../../internal/chatimage/filter.go#L1-L212) — `Placement`, `Registry`, `diacriticIndex`, `FilterLine`, `escapeLen`, `NeutralizeText`; tests in filter_test.go.
2. <a id="source-2"></a> [frames.go](../../internal/tui/frames.go#L40-L83) — `consoleDisplayFrame`, `sessionFrame`, `filterImages`; `View` captures in tui.go.
3. <a id="source-3"></a> [image_filter_test.go](../../internal/tui/image_filter_test.go#L1) — owner, forging pane, console, composed view, release and a placeholder title; `emulator.Title` and the browser popup URL use `NeutralizeText`.
4. <a id="source-4"></a> [protocol.go](../../internal/chatimage/protocol.go#L1-L285) — `Frame`, `Validate`, `Conn.Send`/`Receive`, `ChannelPair`, `InheritedChannel`; tests in protocol_test.go.
5. <a id="source-5"></a> [session.go](../../internal/tui/term/session.go#L89) — `Options.OnImage`, channel setup in `Start`, reader, close in `pump`, `SendImage`; image_channel_test.go with a real child.
