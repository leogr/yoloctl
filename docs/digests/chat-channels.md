# Managed channel catalog and stream

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `chat channel list/create/add/remove` use finite typed controller requests,
ordinary admission/receipts and the existing verified host or operator principal.
Mutations take native space locks and acknowledge the durable effect gate;
list uses at most two lock-free observations in one five-second context (earlier
caller deadline wins), retrying only unequal native snapshots. Exact room/disk/human
IDs remain bound to the first snapshot; identity change, generation regression,
strict-store/authority failures and cancellation never retry. Each attempt repeats
authority/store/runtime checks; rejected evidence is discarded. `chat.status` stays
single-attempt. This controller behavior requires `space start --update-controller`.
[26](#source-26) [27](#source-27) CLI add/remove resolve a box name once to immutable
room/channel/member/box IDs and a desired generation. A race or lost mutation
reply is an error, never an automatic retry. List is observational and reports
desired catalog, authenticated identity map, `runtime_applied` and pending
reasons. Incomplete inventory cannot authorize a mutation. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] `chat.channel.catalog` instead reads exactly one verified atomic room
manifest, with no runtime scan, private render/credential read, mutation lock,
repair or retry. Its empty typed request rejects all fields. Host/operator access
matches list; current operator grant/incarnation checks remain independent of
the captured room. The desired-only response includes room/disk/generation,
channels/deletion IDs, trusted identities and `runtime_unobserved: true`, with
no `runtime_applied` or pending claims. Missing rooms require explicit ensure;
strict storage/identity and cancellation errors remain failures. CLI
`chat channel list --catalog-only` explicitly selects this response; ordinary
list, status, mutation preflight and completions retain observation.
[28](#source-28) [29](#source-29) [30](#source-30) [32](#source-32)

[FACT] Native schema 2 stores at most 16 immutable channel IDs/names and sorted
member-ID ACLs. Names are canonical lowercase ASCII `#[a-z0-9_-]+`, 2–48 bytes;
the CLI accepts an optional `#` and folds ASCII case. `#space` remains first and
all channels include the human. Schema-1 reads project a deterministic default
channel ID without writing. Only an effective explicit channel mutation upgrades
the schema; checked no-ops retain bytes/generation. Rename and changing the
human ACL are unavailable; [deletion](chat-channel-deletion.md) is a separate
explicit operation. [4](#source-4) [5](#source-5)

[FACT] Creation snapshots currently enrolled members. Later enrollment adds only
`#space`; add/remove edit an existing member's channel permission independently
of room enrollment. Grants require the matching current declaration; removal
can reduce an active enrolled member's ACL after deletion or name replacement,
while retaining exact room/channel/member/box IDs and generation checks.
Room revocation removes that immutable ID from every ACL;
reuse of a box name cannot inherit it. Rendering intersects each ACL with active
accounts and controller-derived operators. Tokens and `client.toml` remain
stable, including startup `#space`; private identity metadata changes. Import
remaps channel/member IDs together on the new disk, preserves names/relationships
and synthetic server data, and generates fresh credentials. [4](#source-4)
[5](#source-5) [6](#source-6) [7](#source-7)

[FACT] Mutations require a verified active holder/container and applied old
configuration before publishing. Server `channel-add-reload` is required. Private
client metadata publishes before server configuration; publication errors retain
desired state. Exactly one reload is followed by status. Its partial
acknowledgment and full status must match the captured incarnation and wanted
generation/digest. Peer/bind verification remains in the shared runtime path.
A lost reply is uncertain even if later status proves application. No restart,
TCP fallback or silent client JOIN accompanies a catalog mutation. [2](#source-2)
[6](#source-6) [8](#source-8) [9](#source-9)

[FACT] The human relay probes `client-topic` and permits bounded single-channel
join/part/topic alongside read/send/history/members/who/typing/quit. Topic text
absent queries, empty clears, and nonempty sets; null, CR/LF/NUL, more than 200
UTF-8 bytes, foreign channels and extra fields fail closed. Each channel request
rechecks the same room/disk/human and desired ACL through a bounded, read-only
atomic native manifest snapshot. This narrow lock-free path retains no-follow,
owner/mode/link, size and strict decoding checks; it neither reads credentials
nor publishes state. Mutations retain native locks; stream setup pins one verified
SSH peer without that lock. AIRC separately
enforces loaded ACLs; desired state is not applied authority. Read/quit take no
controller lock. [10](#source-10) [11](#source-11) [18](#source-18)

[FACT] `RunChannels` owns one JSONL v2 stream/cursor, one pending send/control
slot and serialized history, preserving caller IDs/channels. Authentic self
JOIN confirms membership then queues history. Topic queries require matching
server/recipient/channel 366 within ten seconds. Late setup cannot clear
quarantine or overwrite newer TOPIC. Set/clear requires exact self-prefix,
channel and text. KICK never autojoins; a KICK during an action pending in that
channel finishes it once as uncertain without ending the stream. Gaps
invalidate membership/topic/roster.
No reconnect/replay occurs. Trusted catalog refresh rejects stale or
equal-conflicting generations and replaces identities without joining. Host
`chatSession.readCatalog` supplies default desired-only refreshes or explicitly
selected observations; UI scheduling is separate. The driver preserves the
unobserved discriminator and clears any applied/pending proof for those reads.
The startup stream header also carries no application proof and starts
unobserved until a catalog read supplies its result. [31](#source-31)
[33](#source-33) Saved
nicknames remain desired preferences: refresh retains the nick the stream
connected with until reconnect for echo/TOPIC/numeric correlation;
the stream's own catalog and generation comparison use the saved preference.
Attachment support is a connection capability probed by
the relay, not room-generation metadata: catalog refresh preserves the live
probe result and cannot enable or disable it; a later image send still passes
the shared exchange and relay filter. [19](#source-19) [24](#source-24) [25](#source-25)
[12](#source-12) [13](#source-13) [14](#source-14) [16](#source-16)

[FACT] Explicit host JOIN carries one bounded trusted refresh, accepted before
admission in the same action envelope. Unknown names get create-first guidance
and no wire JOIN. Join refusal codes are `unknown-channel` and `acl-denied`
(403). Authentic `422 No MOTD` is benign after server/recipient checks, never
a history gap or unrelated control refusal.
[13](#source-13) [20](#source-20) [21](#source-21) [23](#source-23)

[FACT] Private-tree, stubbed-control and JSONL regressions cover identities,
ACL/import, snapshots, interruption, locks, history and open-stream refresh.
UI/host/relay fixtures join a channel created after opening and confirm a send
on legacy/schema-2 rooms. They do not prove VM confinement, live cgroup/bind
authority or host terminal behavior. [5](#source-5) [22](#source-22)
Separate opt-in tests against real AIRC binaries are summarized in
[upstream contracts](airc-upstream.md).
[7](#source-7) [9](#source-9) [15](#source-15) [17](#source-17)
[19](#source-19)

## Sources

1. <a id="source-1"></a> [CLI](../../cmd/yoloctl/chat_channels.go#L48) — command construction, immutable resolution, catalog refresh.
2. <a id="source-2"></a> [Controller](../../cmd/yoloctl/chat_channels_runtime.go#L22) — strict requests, authority, apply transaction.
3. <a id="source-3"></a> [Registry](../../cmd/yoloctl/controller_internal.go#L36) — `controllerOperations`, channel dispatcher.
4. <a id="source-4"></a> [Catalog](../../internal/managedchat/channels.go#L18) — validation, mutation, enrollment and remapping.
5. <a id="source-5"></a> [Native regressions](../../internal/managedchat/channels_test.go#L21) — legacy, ACL, interruption and publication.
6. <a id="source-6"></a> [Rendering](../../internal/managedchat/render.go#L196) — `buildRender`, `renderChannels`.
7. <a id="source-7"></a> [Archive regression](../../internal/spacearchive/chat_channels_test.go#L16) — independent credentials/ACLs and recovery.
8. <a id="source-8"></a> [Reload proof](../../internal/managedchat/channel_control.go#L9) — partial acknowledgment and incarnation checks.
9. <a id="source-9"></a> [Controller regressions](../../cmd/yoloctl/chat_channels_test.go#L16) — requests, selectors and one-reload proof.
10. <a id="source-10"></a> [Relay](../../cmd/yoloctl/chat_stream.go#L88) — feature probe and per-operation authorization.
11. <a id="source-11"></a> [Protocol](../../internal/chatstream/protocol.go#L104) — request decoding, `FilterChannels`.
12. <a id="source-12"></a> [Driver](../../internal/chatstream/channels_client.go#L52) — `RunChannels`, deadlines and gaps; `run` keeps the saved nickname in its catalog (L81-L86).
13. <a id="source-13"></a> [Controls](../../internal/chatstream/channel_events.go#L30) — actions, membership and topic evidence.
14. <a id="source-14"></a> [Refresh](../../internal/chatstream/channel_catalog.go#L95) — `receiveCatalog`, `refreshCatalog`.
15. <a id="source-15"></a> [Stream regressions](../../internal/chatstream/channels_test.go#L46) — topic/control/history/ACL.
16. <a id="source-16"></a> [History](../../internal/chatstream/channel_history.go#L19) — page queue/correlation.
17. <a id="source-17"></a> [Open-stream regression](../../internal/chatstream/channels_loop_test.go#L139) — identity refresh.
18. <a id="source-18"></a> [Native snapshot](../../internal/managedchat/store.go#L61) — `ReadManifestSnapshot`, shared private manifest reader; mutation callers retain locks.
19. <a id="source-19"></a> [Host shape regression](../../cmd/yoloctl/chat_catalog_refresh_test.go#L18) — actual builders, JSON and `RunChannels` for legacy/schema-2 catalog generations and a nickname preference saved after the stream connected.
20. <a id="source-20"></a> [Admission regressions](../../internal/chatstream/channel_admission_test.go#L10) — No MOTD, catalog ordering/conflicts, create-first and real refusal correlation.
21. <a id="source-21"></a> [Host admission](../../cmd/yoloctl/chat_view.go#L598) — `control` captures link and refresh.
22. <a id="source-22"></a> [Wire fixture](../../cmd/yoloctl/chat_topic_wire_test.go#L26) — JOIN after opening and confirmed send.
23. <a id="source-23"></a> [Admission tests](../../cmd/yoloctl/chat_join_admission_test.go#L13) — refresh, link and codes.
24. <a id="source-24"></a> [Connection capability](../../cmd/yoloctl/chat_stream.go#L52) — the confined-client feature probe sets attachment support independently of room metadata.
25. <a id="source-25"></a> [Refresh send regression](../../internal/chatstream/catalog_attachment_send_test.go#L15) — image send, filter and echo through the real exchange after a newer catalog.

26. <a id="source-26"></a> [Read attempts](../../cmd/yoloctl/chat_observation.go#L28-L129) — `observeManagedChatRead`, `observeManagedChatCatalog`, binding and strict attempt.
27. <a id="source-27"></a> [Retry tests](../../cmd/yoloctl/chat_catalog_retry_test.go#L18) — churn, revocation, runtime-only change, budgets, identity and authority.

28. <a id="source-28"></a> [Desired catalog](../../cmd/yoloctl/chat_catalog.go#L15) — `readDesiredChatCatalog`, pure DTO projection, mode validation and captured diagnostics.
29. <a id="source-29"></a> [Desired-read regressions](../../cmd/yoloctl/chat_catalog_test.go#L42) — one native read, concurrent replacement, strict errors, routing/coalescing and legacy list output.
30. <a id="source-30"></a> [Operator regression](../../cmd/yoloctl/chat_catalog_operator_test.go#L24) — live grant/incarnation validation and one manifest read through controller framing.

31. <a id="source-31"></a> [Driver catalog](../../internal/chatstream/channel_catalog.go#L110) — `refreshCatalog`, runtime-unobserved normalization.
32. <a id="source-32"></a> [Operator authority](../../cmd/yoloctl/controller_operator.go#L28) — `operatorRuntime`, `checkOperatorPrincipal`.

33. <a id="source-33"></a> [Startup catalog](../../internal/chatstream/channels_client.go#L112) — header projection explicitly carries no runtime observation.
