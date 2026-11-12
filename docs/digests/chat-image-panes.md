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
image or a click on the rows releases it by its token. An error reply
collapses it with a short reason. A ready reply for another request, or after
the request was replaced, is released at once. A new capability generation or
withdrawn support collapses a shown preview, and so do a reduced colour
profile, a lost connection and any membership state other than joined for the
image's channel; collapsing releases a shown image. [1](#source-1) [3](#source-3)

[FACT] Literal placeholder characters in message text display as spaces, so
only a pane's own preview rows draw image cells; copy, Quote and the source
copy keep the original text. [1](#source-1) [3](#source-3)

## Sources

1. <a id="source-1"></a> [preview_image.go](../../internal/chatui/preview_image.go#L1) — `PreviewCapabilityMsg`, `PreviewReadyMsg`, `PreviewErrorMsg`, `previewArtifact`, `canPreview`, `togglePreview`, `releasePreview`, `previewUpdate`, `previewRows`; the `trueColor` field, message action, `actPreview` and the transcript call in model.go, mouse.go, transcript.go.
2. <a id="source-2"></a> [chat_image_preview.go](../../cmd/yoloctl/chat_image_preview.go#L1) — `claimChatPreviews`, `configure`, `relay`; chat_view.go wiring; chat_image_preview_test.go.
3. <a id="source-3"></a> [preview_image_test.go](../../internal/chatui/preview_image_test.go#L1) — action gating, request fields, placeholder rows and colour, hide, errors, stray ready, capability change, reduced colour profile, lost connection or membership, literal placeholders.
