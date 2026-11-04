# Native chat composer image drafts

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Ctrl+V asks the host's `ReadImage` callback for one clipboard PNG; with
no callback (`seams.clipboard` off or unsupported) nothing is read. One read
is outstanding, answered by its ID. Ctrl+V first inserts an `[Image #n]`
placeholder at the cursor of that channel's draft, so text typed while the
host reads stays after it; the answer fills it, and a failure removes it,
keeping the text around it. Nothing is sent while an image is being read.
[1](#source-1) [2](#source-2)

[FACT] A placeholder is an editor fold whose bytes are only its label and
whose undo snapshots share one payload pointer. It moves and deletes as one
unit like a collapsed paste but never expands, undo/redo restore or remove that
same image, and kills never yank its label as text. Images and pastes are
numbered separately within a draft, and each channel keeps its own. At most 8
images of up to 4 MiB are held across drafts, including ones only undo could
restore; those give way first to a new paste. The chip is styled by state
(italic while read or sent, yellow while uncertain); a click puts the cursor
after it and describes it. [3](#source-3) [1](#source-1) [4](#source-4)

[FACT] Enter, with the usual send checks, sends a draft without images as one
message. With images, a room whose AIRC stages them gets one message instead
(see [staged images](chat-staged-images.md)). Otherwise the leading part goes first: text before the first image
(trailing blanks trimmed) as a message, or that image through `UploadImage`;
one text send and one upload are outstanding at a time. A confirmed part leaves
the draft (an image with the blank rows after it, keeping indentation), and
the next part follows only if draft and reply target are unchanged; otherwise
Enter sends the rest. Remainders after sent parts, refused uploads or
announcement-resolved images are messages, never slash commands; undo snapshots keep that mark, so clearing and
undoing or redoing it cannot turn it into one. No edit, kill or undo removes an image in
flight. Removing a confirmed part ends undo history, so undo cannot restore
that part; a refused or spent image leaves history too. Text edited during
its send stays in the draft without automatic continuation, as for ordinary
messages. Failed clipboard reads and manually deleted uncertain placeholders
do not newly mark surrounding text as a remainder. [5](#source-5)
[3](#source-3) [1](#source-1)

[FACT] The host spends a token per attempt, so Rejected drops the image (paste
again to retry) and keeps the rest. Uncertain, or a disconnect during the
upload, keeps its placeholder, blocks sending in that channel, names the host
operation to inspect (`yoloctl space operation`) and never retries or
continues; deleting it records the attempt. A late result for the token, or an
announcement whose reserved artifact metadata matches this account, generated
name, size and SHA-256 (even one seen before the outcome), resolves it without
acknowledging the operation; text alone never does, and the rest waits for
Enter. Operation guidance leads with the inspect command, also for a later
outcome of a published or removed image, and is kept as a wrapped note in its
channel and shown even from a hidden one. Artifact messages render as an inert
`[file NAME · SIZE]` chip with Copy file id; source view keeps the original
text but no link target. [1](#source-1) [6](#source-6)

[FACT] Regressions cover reads and their bounds, inline order with typed text
and pastes, undo/redo and cuts, per-channel drafts with a hidden channel
continuing, ordered part sends, in-flight protection, edits and refusals
mid-send, command suppression across undo, history boundaries, rejection, uncertainty, disconnects, late
outcomes and artifact chips. They use fake host callbacks; real clipboard reads
and publication need a host check. [6](#source-6) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Image drafts](../../internal/chatui/attachments.go#L19-L446) — `pasteImage`, `roomForImage`, `imagePrepared`, `dropImage`, `settleImages`, `uploadImage`, `imageUploaded`, `leadingBlank`, `imageUnresolved`, `lostImage`, `published`, `imageChipStyle`; wrapped notes in presence.go `presenceRows`.
2. <a id="source-2"></a> [Image contract](../../internal/chatui/images.go#L1) — `ImageRequest`, `ImagePreparedMsg`, `ImageUpload`, `ImageUploadedMsg`, `ArtifactRef`; host wiring in `cmd/yoloctl/chat_image_host.go` (`configureChatImages`).
3. <a id="source-3"></a> [Image folds](../../internal/chatui/editor.go#L23-L529) — `editState.remainder`, `fold`, `replace`, `remove`, `kill`, `restore`, `expandFold`, `insertImage`, `held`, `commit`, `forget`.
4. <a id="source-4"></a> [Chips and clicks](../../internal/chatui/view.go#L594-L633) — `inputRow`; `chipLabel` in render.go, `hitFold` in mouse.go `clickHit`, `hoverHint` in hover.go.
5. <a id="source-5"></a> [Ordered sends](../../internal/chatui/model.go#L335) — `Update` (settle and resume), `submit`, `sendDraft`, `slashCommand`, `delivery`.
6. <a id="source-6"></a> [Image regressions](../../internal/chatui/attachments_test.go#L82) — reads, bounds, ordered parts, in-flight protection, edits mid-send, uncertainty, artifact chips.
7. <a id="source-7"></a> [Composer regressions](../../internal/chatui/composer_test.go#L13) — inline order, undo/redo and cuts, per-channel drafts, remainder provenance, confirmed-part history boundaries.
