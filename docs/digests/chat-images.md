# Managed chat image publication

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] Host clipboard preparation and publication are separate callbacks.
Clipboard-off, unavailable readers and operator endpoints install neither
callback. Preparation captures the room/channel and current stream before the
desktop read, admits one read at a time, and returns a generated
`paste-<random-token>.png` name. Source paths and path-bearing desktop errors
are discarded; no-image remains distinguishable. Preparation performs no
upload. The host retains at most 32 metadata records, usable for ten minutes,
without retaining their PNG payloads. Reconnection invalidates them; an upload
attempt consumes its record before dispatch. [1](#source-1)

[FACT] `chat.artifact.upload` is a host-only, effect-gated controller mutation.
Its metadata contains immutable room, disk, human and channel IDs, canonical
channel name, random publication token, byte length and SHA256, plus an optional
stage flag for [staged images](chat-staged-images.md). It accepts no
file path, URL, caller argv or terminal/session options. Stdin carries one
four-byte big-endian length followed by exactly that many bytes and EOF.
Both host and controller require a nonempty PNG of at most 4 MiB; the existing
PNG validator also bounds decoded pixels and checks the complete image.
The controller verifies the length and hash before opening the upload path.
[2](#source-2) [3](#source-3) [4](#source-4) [9](#source-9)

[FACT] Publication uses the shared verified, pinned managed-chat connection,
probes `artifact-cli` and `artifact-metadata`, rechecks the connection and
current native room/channel identity, then obtains the durable effect permit.
It runs fixed confined argv:
`airc artifact upload --config /run/yoloctl/chat-server/human/client.toml --channel
<canonical-channel> --name <generated-name> --stdin`.
Bytes flow through a private pipe; there are no staging mounts or extra IRC
client commands. No native space mutation lock is acquired by this path.
The controller deadline is 45 seconds, and cancellation closes its owned input;
the host deadline is 50 seconds. Current identity/access is required, while a
newer desired generation alone does not invalidate the prepared image.
[2](#source-2) [3](#source-3) [1](#source-1)

[FACT] The confined reply is limited to 8 KiB. Artifact and message IDs must be
canonical UUIDs, the URL must match the fixed artifact origin and ID, and
SHA256/size must match the input. The host receives only IDs, size, hash,
request ID and publication token, and validates that correlation again.
Results are live stdout; durable receipts exclude payloads and stdout.
After a possibly launched upload, errors, timeouts and invalid replies produce
`EffectsUncertain`; HTTP status alone never proves rejection. Host transport
errors likewise remain uncertain. There is no automatic replay; inspection uses
the operation ID and the generated token in authenticated channel history.
[2](#source-2) [3](#source-3) [5](#source-5)

[FACT] Stream messages expose generic attachment metadata only from a complete,
valid set of reserved `airc-artifact`, `airc-artifact-name`,
`airc-artifact-bytes` and `airc-artifact-sha256` tags on an authenticated
`PRIVMSG`. Message text, URLs and client-only `+` tags confer no attachment
authority. Metadata survives the driver's live/history paths; filenames do
not prove media type. No host fetch or image preview is implied.
[6](#source-6) [10](#source-10)

[FACT] Focused regressions cover malformed/oversized PNGs and frames, immutable
identity changes, clipboard policy and concurrent reads, stale/consumed
drafts, fixed stdin argv, bounded/forged replies, and no replay. A real local
controller-wire/receipt fixture distinguishes cancellation before the effect
gate from a lost reply after possible publication and checks the recovery
fence. These fixtures do not establish desktop gesture routing, real OpenShell
authority, AIRC HTTP interoperability or provider image consumption.
[7](#source-7) [8](#source-8) [11](#source-11) [12](#source-12) [13](#source-13)

## Sources

1. <a id="source-1"></a> [Host callbacks](../../cmd/yoloctl/chat_image_host.go#L22) — `configureChatImages`, `imageLinkLive`, `chatImageIdentity`, lines 22–202.
2. <a id="source-2"></a> [Typed protocol](../../cmd/yoloctl/chat_image_protocol.go#L21) — request validation, framing, identity and reply checks, lines 21–167.
3. <a id="source-3"></a> [Controller publisher](../../cmd/yoloctl/chat_image_runtime.go#L24) — `runControllerChatImage`, `executeChatImageUpload`, `runChatImageCommand`, lines 24–138.
4. <a id="source-4"></a> [PNG bounds](../../internal/hostintegration/host.go#L48) — `ValidateImage`.
5. <a id="source-5"></a> [Receipt contents](../../internal/controller/receipts.go#L17) — `Receipt`.
6. <a id="source-6"></a> [Reserved metadata](../../internal/chatstream/artifact.go#L8) — `Artifact`, `artifactFromTags`.
7. <a id="source-7"></a> [Protocol regressions](../../cmd/yoloctl/chat_image_protocol_test.go#L46) — metadata, framing, identity and reply validation.
8. <a id="source-8"></a> [Receipt boundary fixture](../../cmd/yoloctl/chat_image_receipt_test.go#L21) — `TestChatImageReceiptSeparatesPreEffectCancellationFromPossiblePOST`.
9. <a id="source-9"></a> [Operation registration](../../cmd/yoloctl/controller_internal.go#L36) — `controllerOperations`.
10. <a id="source-10"></a> [Stream mapping](../../internal/chatstream/client.go#L654) — authenticated message adaptation.
11. <a id="source-11"></a> [Host regressions](../../cmd/yoloctl/chat_image_host_test.go#L33) — clipboard authority, stale drafts, bounds, outcome correlation and no replay.
12. <a id="source-12"></a> [Upload regressions](../../cmd/yoloctl/chat_image_runtime_test.go#L36) — fixed argv, preflight rejection, bounded outcomes and effect gate.
13. <a id="source-13"></a> [Metadata regressions](../../internal/chatstream/artifact_test.go#L18) — reserved tags and replay.
