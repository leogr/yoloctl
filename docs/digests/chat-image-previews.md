# Chat image previews (host boundary)

Verification: `484b52b6eb83b3bc03f30406d28110f262fb5179` and accompanying changes; image channel on `b96ffc26c67ed63006d0dfa55a1b9c228d8e18ba` and accompanying changes; host probe on `d8253d1809006bf3095a1215b7a9c1785523ddd1` and accompanying changes; broker on `56379f45f1d89b34015f180380ef9391a31760b0` and accompanying changes; broker conditions and channel identity on `765d227c0610c02b4d68071cd34552086b32eab1` and accompanying changes.

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
ready and error; the child sends preview (validated room, disk, human and
channel identities, channel name, message, artifact UUID, hash and 1 B–4 MiB
size) and
release (a host-issued token). Fields of another operation, unknown keys,
trailing data or the wrong direction close the channel; the session itself
continues, and closing it never joins a blocked handler. The child claims the
descriptor once and removes the variable. Frames carry no image bytes, paths,
URLs, argv or terminal sequences. [4](#source-4) [5](#source-5)

[FACT] Only the outer TUI, which owns the host terminal, probes it, at most
once and never again after an outcome. It sends XTVERSION and a non-storing
Kitty graphics query (one pixel, action query) with an ID from its own
randomly seeded 24-bit sequence. Previews are supported only if, within 1 s,
the version reply names Kitty 0.28.0 or later or Ghostty 1.3.1 or later, the
reply to that query ID is `OK`, and cell pixels are measured; support is
withdrawn while the measurement is missing. A refusal, another terminal
(libghostty in a nested TUI included) or silence ends the probe unsupported; once the budget is over nothing late
counts, and a cell measurement re-checks the probe as soon as it arrives.
[6](#source-6) [7](#source-7) [8](#source-8)

[FACT] The TUI gives the channel only to native chat panes. Their frames
wait in a 16-frame inbox until the event loop drains it; frames to panes
leave through a 64-frame outbox written in order by one sender goroutine with
a 1 s write deadline; overflow is dropped and the pane keeps its chip, and a
failed write closes that pane's channel so it cannot delay the others. The
first chat pane starts the probe; each live pane is sent the capability once
per generation (counted only when the frame was queued), which changes when support changes. Preview requests wait
(at most 8) for the probe, then get `stale` for an old generation,
`unsupported`, or `unavailable`: no fetch or drawing exists yet. A release
removes a preview only with its token, and a pane that ends releases its
registration. The chat child claims the channel before anything else and,
for now, discards host frames. [9](#source-9)

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
6. <a id="source-6"></a> [detect.go](../../internal/chatimage/detect.go#L1) — `ParseTerminalVersion`, `Terminal.Supported`, `Probe`; ids.go `IDs`; detect_test.go.
7. <a id="source-7"></a> [image_probe.go](../../internal/tui/image_probe.go#L1) — `startImageProbe`, `imageProbeMsg`, `checkImageProbe`, `imagesSupported`; image_probe_test.go.
8. <a id="source-8"></a> [host_geometry.go](../../internal/tui/host_geometry.go#L39) — `acceptHostCellSize` re-checks the probe.
9. <a id="source-9"></a> [image_broker.go](../../internal/tui/image_broker.go#L1) — `imageHandler`, `sendImage`, `syncImages`, `answerPreview`, `chatImageHandler`; `Registry.ReleaseToken`; `Session.SendImage` deadline; chat_view.go claim; image_broker_test.go.
