# Chat image panes

Verification: `f827038f1a2de48ddaec5e467510e3532fa1b29a` and accompanying changes.

[FACT] A native chat pane claims the TUI's image channel first thing. Its
bridge relays the host's capability, ready and error frames to the chat model
and, when the channel ends, withdraws support. A message offers Show image only
while the host advertises support and the pane's colour profile is truecolor
(a reduced profile would rewrite the placeholder colour that names the image)
and the pane is connected and may send to its channel, and only for a message with a server
message ID and one artifact (a server artifact or a single published
attachment). Asking captures the room, disk, human and channel identities at
that moment and sends one preview request with the artifact tuple and the
`[file NAME · SIZE]` chip text. [1](#source-1) [2](#source-2)

[FACT] A pane has at most one preview: showing another first releases the
current one. On a ready reply for its own request and generation the pane
draws the preview below the message as decor rows of placeholder cells
(U+10EEEE with row and column marks) in the image ID's 24-bit colour; Hide
image or a click on the rows releases it by its token. While Show or Hide
image applies, the message's own `[file NAME · SIZE]` chip carries the same
toggle as a click target; the footer action remains for the keyboard. File
chips enter the parsed body as one-rune private-use tokens the message does
not contain and are then replaced with their literal labels, filtered for
placeholder characters like message text, so a file name is never read as
Markdown, a typed look-alike stays text, copy yields the label and a wrapped
chip keeps the action on every row. A chip inside a table, whose column
widths are fixed when parsed, enters as a run of its token as wide as its
label, so the table is laid out for the label, and the run is then swapped
for the literal label; it shows exactly, inside code or emphasis too, but
without the click action, and the footer action still applies (scope
accepted by the coordinator). Rows drop the action where no preview applies. An error reply
collapses it with a short reason. A ready reply for another request, or after
the request was replaced, is released at once. A new capability generation or
withdrawn support collapses a shown preview, and so do a reduced colour
profile, a lost connection and any membership state other than joined for the
image's channel; collapsing releases a shown image. [1](#source-1) [3](#source-3)

[FACT] Literal placeholder characters in message text display as spaces, so
only a pane's own preview rows draw image cells; copy, Quote and the source
copy keep the original text. [1](#source-1) [3](#source-3)

[FACT] Standalone `yoloctl chat` is its own image host when it has no TUI
image channel, `YOLOCTL_TUI` is not `1` and `seams.images` is on; inside a TUI
without a channel it shows chips and never probes its tab's emulated terminal.
At start it sends the same version, non-storing graphics and CSI 16t queries
(again on focus) and advertises the outcome as generation 1. It answers
requests like the TUI broker: stale, unsupported, a cache hit, busy while its
single 30 s read/decode job runs, then `not_png`, `too_large` or `failed`. It
reads through the same host controller reader for the view's space, prepares
at 60×12 cells, deletes its previous image, transmits and replies ready only
after the transmit is written. A changed cell size starts a new generation and
empties the cache; a release with the shown token or exit deletes the image.
Every frame passes the TUI's placeholder filter, so only the shown preview's
cells display. [4](#source-4)

## Sources

1. <a id="source-1"></a> [preview_image.go](../../internal/chatui/preview_image.go#L1) — `PreviewCapabilityMsg`, `PreviewReadyMsg`, `PreviewErrorMsg`, `previewArtifact`, `canPreview`, `togglePreview`, `releasePreview`, `previewUpdate`, `previewRows`, `chipText`, `placeChips`, `literalChipLines`, `swapText`; the `trueColor` field, message action, `actPreview` and the transcript call in model.go, mouse.go, transcript.go.
2. <a id="source-2"></a> [chat_image_preview.go](../../cmd/yoloctl/chat_image_preview.go#L1) — `claimChatPreviews`, `configure`, `relay`; chat_view.go wiring; chat_image_preview_test.go.
3. <a id="source-3"></a> [preview_image_test.go](../../internal/chatui/preview_image_test.go#L1) — action gating, request fields, placeholder rows and colour, hide, errors, stray ready, capability change, reduced colour profile, lost connection or membership, chip click toggle and its absence where no preview applies, generated chip only (not a typed look-alike), literal file names, copy and wrapping, label placeholder filter, literal inert table chips (plain, code, emphasis; column alignment), literal placeholders.
4. <a id="source-4"></a> [chat_image_standalone.go](../../cmd/yoloctl/chat_image_standalone.go#L1) — `ownsTerminalImages`, `standaloneImages` (`start`, `update`, `checkProbe`, `advertise`, `acceptCellSize`, `answer`, `finish`, `show`, `release`, `filter`, `close`), `standaloneChat`; chat_view.go wiring; chat_image_standalone_test.go.
