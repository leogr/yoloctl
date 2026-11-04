# Native chat: text and images as one message

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] The controller's stream probe sets the room header's `attachments` only
when the confined AIRC client lists `message-attachments`. Only then does the
controller filter forward a send's `attach` list, and the host driver accept it;
without the feature an `attach` field is refused before it can reach AIRC as an
unknown field. The connection event carries the flag to the view. Images sent
from a room without it still go as ordered parts.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The bundled AIRC pin, `0c2c8bd4`, lists `message-attachments`. A room
offers staging only once its server and the human's client run an image built
from a pin with it; older images keep sending ordered parts. [11](#source-11)

[FACT] With the flag, Enter on a draft with images stages them one at a time
in draft order (`UploadImage{Stage:true}`, host token spent per attempt), then
sends the whole draft as one `Outgoing`. Its text holds each image as
`[Image #k]`, numbered 1..N in draft order, and `attach` lists each staged ID at
its marker's byte offset. A stage binds only when the reply is a stage of
exactly that image's size and SHA-256. The next part follows only if the draft
and reply are unchanged; otherwise Enter continues from the current draft, so a
removed or moved image never attaches. Identical pastes stage separately.
[4](#source-4) [5](#source-5)

[FACT] Outcomes never retry. A host refusal before dispatch (spent or expired
token, changed identity, invalid bytes) or a confirmed reply that is not a stage
of exactly that image marks it `expired`: its placeholder and the rest of the
draft stay, and Enter waits for a fresh Ctrl+V. Every failure of the confined
staging command, including AIRC's definite quota refusal, is an uncertain
controller operation: its stderr is discarded and no certainty is recovered
from text, so the image stays uncertain behind the operation fence, with
guidance that staging never announces it. For the one message: Confirmed
clears an unchanged draft and removes its images for good; Uncertain freezes
their IDs; `unknown k=K` expires only that image, and only if no send of the
draft was uncertain; `consumed` counts as sent only when the named message in
loaded history is exactly this submission (this account, text, reply, IDs and
offsets): its images leave the draft, which clears only if neither it nor its
reply changed meanwhile. Otherwise it freezes the IDs as possibly sent;
`unavailable` keeps everything; `store`, `too-long`, `invalid` and
`channel-only` keep the draft, reply and staged references with their own
notice, and the refused message keeps the raw receipt. A `store` notice offers
Enter with the same references while they remain available, promising neither
success nor that nothing was sent, and asks for history first after an
uncertain send. Any other refusal shows its reason. A send the stream refused before writing it carries no AIRC
refusal: the draft, reply, staged IDs and bytes stay unfrozen, and only an
explicit Enter sends them again, without restaging. [12](#source-12) [13](#source-13)
After a definite `consumed` refusal Enter is available again with
the same frozen IDs: the server refuses their reuse, and nothing is resent or
staged again automatically. While the send itself is uncertain Enter stays
blocked until its late matching echo resolves it in the view; otherwise
restarting `yoloctl chat` drops the in-memory draft while its staged copies
expire.
[4](#source-4) [5](#source-5) [6](#source-6) [9](#source-9)

[FACT] Received messages carry attachments only from AIRC's reserved
`airc-artifacts` tag on an authenticated PRIVMSG, all entries valid and each
marker at its offset; an echo confirms a send only with the same IDs and
offsets, and a send with images reported confirmed without that echo becomes
uncertain. The view draws each verified span as a `[file NAME · SIZE]` chip; a
typed look-alike stays text, source view shows the original, and one "Copy file
ids" message action copies one ID per line (chips have no hit target of their
own). Refusals are parsed only from AIRC's exact
`FAIL … ATTACHMENTS … attachments: <reason>` receipt.
[2](#source-2) [4](#source-4) [7](#source-7)

[FACT] `chat.artifact.upload` with `stage` requires the feature in the
controller's probe, adds `--stage` to the fixed argv and accepts only a staged
reply (no URL or msgid, matching size and digest). Any other reply or failure
after launch is `EffectsUncertain`, as for uploads.
[8](#source-8) [9](#source-9)

[FACT] Pinned AIRC publishes a message and its stages in one transaction and
reports any failure of it as `store`. With no temporary directory set, SQLite
falls back to `/var/tmp`, which the confined baseline does not grant, so the
managed server's children get `/tmp` instead, applied by recreating its
container ([runtime](chat-runtime.md)). [14](#source-14) [15](#source-15)
[16](#source-16) [17](#source-17)

[FACT] Regressions cover feature gating and canonical forwarding, the tag
grammar, refusal parsing, echo identity, ordered staging and renumbering, edits
during staging, identical pastes, expiry, uncertainty, each final refusal
(including an explicit retry of a `store` refusal without restaging), inline
rendering and the legacy fallback. Composer outcomes use injected host
callbacks, so they do not prove the live refusal path; staging against a live
AIRC needs a host check. [6](#source-6) [7](#source-7) [10](#source-10)

## Sources

1. <a id="source-1"></a> [Stream gate](../../cmd/yoloctl/chat_stream.go#L44-L101) — `header.Attachments`, `managedChatAttachments`, `relayManagedChatWithExec`.
2. <a id="source-2"></a> [Attachment grammar](../../internal/chatstream/attachments.go#L1-L122) — `ValidAttach`, `attachmentsFromTags`, `SameAttachments`, `ParseAttachmentRefusal`.
3. <a id="source-3"></a> [Request allowlist](../../internal/chatstream/protocol.go#L104-L228) — `decodeChannelRequest` attach gate, `FilterChannels`.
4. <a id="source-4"></a> [Staged composer](../../internal/chatui/staging.go#L1-L278) — `stagedDraft`, `sendStaged`, `imageStaged`, `expire`, `stagedOutcome`, `stagedRefusal`, `publishedAs`, `attachmentText`.
5. <a id="source-5"></a> [Composer hooks](../../internal/chatui/model.go#L1217-L1378) — `sendDraft` staged branch; `delivery` passes the submitted message to `stagedOutcome`; staging branches in attachments.go `imageUploaded`, `imageUncertainText`, `imageUnresolved`.
6. <a id="source-6"></a> [Composer regressions](../../internal/chatui/staging_test.go#L1-L406) — ordered stages, renumbering, edits, fresh paste, uncertainty, refusals including consumed proof and in-flight reply edits, rendering, legacy fallback, identical pastes.
7. <a id="source-7"></a> [Stream regressions](../../internal/chatstream/attachments_test.go#L1-L171) — offsets, reserved tags, refusals, forwarding, echo identity.
8. <a id="source-8"></a> [Staging request](../../cmd/yoloctl/chat_image_protocol.go#L24-L188) — `Stage`, `decodeChatImageStageReply`, `decodeChatImageResult`; host `UploadImage` in chat_image_host.go.
9. <a id="source-9"></a> [Staging runtime](../../cmd/yoloctl/chat_image_runtime.go#L68-L150) — feature requirement and `--stage` argv.
10. <a id="source-10"></a> [Controller regressions](../../cmd/yoloctl/chat_image_staging_test.go#L1-L124) — feature gate, argv, non-stage replies uncertain, probe flag, view conversion.
11. <a id="source-11"></a> [Source pin](../../internal/features/airc-source/manifest.json#L1) — AIRC release source commit and archive checksums; the pinned `src/lib.rs` L38 lists the feature.
12. <a id="source-12"></a> [Pre-write refusal](../../internal/chatstream/client.go#L320-L411) — `localRejection` from `exchange` becomes a `rejected` delivery in `driver.send`; chat-runtime.md has the stream contract.
13. <a id="source-13"></a> [Rejection regression](../../internal/chatui/prewrite_rejection_test.go#L12-L57) — `TestPreWriteRejectionKeepsTheStagedDraftForAnExplicitResend`.
14. <a id="source-14"></a> [Server temporary directory](../../internal/agentbox/chat.go#L153-L158) — `chatServerEnvironment`; the baseline's writable paths in compose.go `baselineFilesystem`.
15. <a id="source-15"></a> [SQLite temporary files](https://www.sqlite.org/tempfiles.html#temporary_file_storage_locations) — the directories SQLite tries, in order.
16. <a id="source-16"></a> [AIRC publication](https://github.com/leogr/airc/blob/0c2c8bd4f46e21a682ddd02083408a54854b2c35/src/store.rs#L217-L268) — `Store::publish` and `commit` at the pin.
17. <a id="source-17"></a> [AIRC refusal](https://github.com/leogr/airc/blob/0c2c8bd4f46e21a682ddd02083408a54854b2c35/src/server/core.rs#L1335-L1399) — `publish` maps lookup and commit failures to `store`.
