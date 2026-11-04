# Native chat channels and live identities

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The chat view keeps one view per channel, embedded as the visible one:
draft/reply, messages/caches, reading position, selection,
history cursor, roster, notices, topic, unread/mention counts,
membership and its [message filter](chat-filter.md). Events for another channel update that view without changing what
is shown; its notices stay hidden unless they report an error. Switching never
sends or reconnects. [1](#source-1)

[FACT] One send or channel action is outstanding at a time across channels. A
send carries its channel, and a confirmation clears only that channel's
unchanged draft. A channel whose membership is joining, left, kicked, deleted or
unknown refuses sending and keeps the draft; nothing is rejoined automatically.
A deleted view stays read-only until chat closes
([deletion](chat-channel-deletion.md)). The legacy single channel counts as
joined when no membership was reported.
[1](#source-1) [2](#source-2)

[FACT] `/channels`, `/join NAME`, `/part [NAME]`, `/topic`, `/topic set TEXT`
and `/topic clear` are recognized only as a whole single-line draft and become
typed actions (`join`, `part`, `topic-read`, `topic-set`, `topic-clear`) with
a random ID. `set` keeps the bytes after one separator; `set clear` sets the
literal word. Only `topic-set` carries text; `topic-clear` carries none, and the
stream driver encodes the clear. `/topic` shows a known topic locally and
queries only an unknown one. A refused action reports its reason; an uncertain one blocks
further actions until reconnect. Older-history requests carry their channel and
ID; a page for another ID is ignored. [1](#source-1) [3](#source-3)
[9](#source-9)

[FACT] `/join` neither switches nor sets membership itself: it reports
"Joining…" and only the stream's reports change membership. The view shows the
channel once the join is confirmed and its self JOIN reported, in either order,
unless the user moved elsewhere. A refusal keeps the stream's state (a
lingering `joining` becomes `unknown`) and removes an empty view the attempt
created; a refused join is not shown as a departure. `ControlMsg.Code`
`unknown-channel` explains `yoloctl chat channel create`, `acl-denied` names
missing access. An uncertain join marks the channel `unknown`. [1](#source-1)
[10](#source-10)

[FACT] Ctrl+G, `/channels` or the band chip opens a channel picker; Alt+1…9
switch by position; Alt+←/→ move to the previous or next channel, unread first.
From 100 columns a channel rail heads the side panel with unread and mention
badges. The band counts unread messages elsewhere, and `#name` suggests channel
names as text. [1](#source-1) [4](#source-4) [5](#source-5)

[FACT] Channel views are bounded at 16. Past 1024 messages, 8 MiB or 65536
logical lines in total, hidden channels lose their oldest messages first, then
the visible one; an unresolved send and every draft are kept, and a trimmed
channel marks where its history stops. A connection gap marks every channel.
[1](#source-1)

[FACT] Trust comes only from the controller's catalog. The host reads it
through `chat.channel.catalog` by default (`--catalog-read=single`) when the
stream starts, every minute while open, on manual refresh, when the view meets
an unknown authenticated account (at most every 15 s), and before explicit JOIN.
The mode is fixed for the view. `--catalog-read=observed` instead selects the
existing list observation/retry route for all those reads; failures never
fall back. Invalid modes refuse before RPC. Opening/reconnecting still uses its
existing readiness proof, independently of refresh mode. [15](#source-15)
[17](#source-17) [18](#source-18) The driver checks room identity and generation
first; the view
also ignores a lower generation. A catalog replaces the view's identity map
and the host's attach member map, so new members become trusted without
reopening and revoked ones stop being trusted or attachable. [1](#source-1)
[6](#source-6)

[FACT] Interactive reads (including JOIN preparation) capture a private 2 KiB
stderr tail, sanitize failure reasons and discard success diagnostics; finite CLI
stderr stays unchanged. [11](#source-11) [12](#source-12)
Read failures and driver refusals each appear once per failure run. Only driver acceptance,
after the view's generation check, recovers that refresh's notice; enqueueing
alone does not. Local read/link identities order equal-generation outcomes and
survive coalescing without entering serialized data. Recovery preserves drafts,
reply/images, focus/scroll, unrelated notices and JOIN refusals.
[6](#source-6) [13](#source-13) [14](#source-14)
Desired-only catalogs explicitly show “Server application not checked; use
chat status” in the channel picker. That discriminator survives host coalescing,
driver events and equal-generation updates; the initial identity header is also
unobserved until a selected read returns. [22](#source-22) It cannot retain old applied proof
or claim that the server has not loaded the catalog. The
[desired read and observed retry](chat-channels.md) need a controller update
(`space start --update-controller`) plus the matching host binary, with no
VM-image replacement. Catalogs are observations: action-time relay ACL checks
and AIRC's loaded ACL remain authoritative. [15](#source-15) [16](#source-16)
[19](#source-19) [20](#source-20) [21](#source-21)

[FACT] `chatstream.RunChannels` uses one-slot queues: send/history/action refuse
overflow; catalogs coalesce compatible snapshots by generation.
Driver catalog, membership, control, topic, history-page and gap events keep
their channel and action ID in the view messages. [6](#source-6)

[FACT] Regressions cover drafts/unread, sends confirmed per
channel, refused sending when not joined, typed commands and blocking
uncertainty, catalog generations and kicks, gaps, aggregate trimming, history
IDs, hidden-channel notices, identity refresh and revocation, and host event
mapping. They use fake callbacks and drivers, not a real AIRC server or VM.
A host fixture sends `/topic` through `RunChannels` and `FilterChannels`
to a scripted JSONL peer on legacy/schema-2 rooms, checking wire fields and
view confirmation. [2](#source-2) [7](#source-7) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Channel views](../../internal/chatui/channels.go#L18-L741) — `channelView`, `within`, `switchTo`, `refreshForUnknown`, `catalog`, `membershipChanged`, `showJoined`, `control`, `controlDone`, `unjoined`, `channelCommand`, `trimAggregate`.
2. <a id="source-2"></a> [Channel regressions](../../internal/chatui/channels_test.go#L26-L273) — drafts, sends, membership, commands, catalog, trimming, identities.
3. <a id="source-3"></a> [Submit and routing](../../internal/chatui/model.go#L385-L1323) — `Update`, `submit`, `slashCommand`.
4. <a id="source-4"></a> [Channel rail](../../internal/chatui/roster.go#L63) — `channelRail`.
5. <a id="source-5"></a> [Channel picker](../../internal/chatui/overlay.go#L729) — `channelPickerRows`.
6. <a id="source-6"></a> [Host channel view](../../cmd/yoloctl/chat_view.go#L166) — `chatSession`, `adoptCatalogFor`, `refreshCatalog`, `refreshLoop`, `control`, `chatViewEvent`, `chatCatalog`.
7. <a id="source-7"></a> [Host regressions](../../cmd/yoloctl/chat_view_test.go#L133-L196) — catalog identities/members and channel events.
8. <a id="source-8"></a> [Topic wire regression](../../cmd/yoloctl/chat_topic_wire_test.go#L26) — `TestChatTopicCommandsReachWireThroughHostAndRelay`.
9. <a id="source-9"></a> [Driver actions](../../internal/chatstream/channel_events.go#L24) — `action` refuses a topic field outside `topic-set` and encodes `topic-clear` as empty text.
10. <a id="source-10"></a> [Join regressions](../../internal/chatui/checkpoint_ui_test.go#L330) — `TestJoinWaitsForTheServerAndRecoversFromRefusal`.

11. <a id="source-11"></a> [Capture](../../cmd/yoloctl/chat_channels.go#L238) — `readChatChannelCatalogTarget`; `chat.go:106–138` keeps finite calls separate.
12. <a id="source-12"></a> [Diagnostic tests](../../cmd/yoloctl/chat_catalog_diagnostics_test.go#L38) — real controller frames, isolated terminal sinks/concurrent tails.
13. <a id="source-13"></a> [Recovery](../../internal/chatui/refresh.go#L1) — `refreshResult`, `refreshAccepted`.
14. <a id="source-14"></a> [Recovery tests](../../internal/chatui/refresh_test.go#L19) — ownership, draft retention, ordering, links and hidden channels.

15. <a id="source-15"></a> [Mode and DTO](../../cmd/yoloctl/chat_catalog.go#L74) — fixed desired/observed mode, desired DTO and captured diagnostics.
16. <a id="source-16"></a> [Rendering](../../internal/chatui/overlay.go#L762) — unobserved output and separate observed-pending wording.
17. <a id="source-17"></a> [View wiring](../../cmd/yoloctl/chat_view.go#L30) — default/single/observed refresh and pre-JOIN route, local correlation and errors.
18. <a id="source-18"></a> [Desired-read tests](../../cmd/yoloctl/chat_catalog_test.go#L42) — mode routing, coalescing, finite framing and stale-action refusal.
19. <a id="source-19"></a> [UI discriminator](../../internal/chatui/channels.go#L266) — `CatalogMsg`, `catalog`, clearing stale applied proof.
20. <a id="source-20"></a> [UI regressions](../../internal/chatui/catalog_unobserved_test.go#L8) — unobserved transitions and refresh ownership.
21. <a id="source-21"></a> [Driver regression](../../internal/chatstream/catalog_unobserved_test.go#L5) — removal of applied/pending claims while preserving local read identity.

22. <a id="source-22"></a> [Startup header projection](../../internal/chatstream/channels_client.go#L112) — initial catalog is unobserved, with no applied/pending claim.
