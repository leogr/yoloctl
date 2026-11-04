# Managed channel deletion

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] `chat channel delete NAME --yes` refuses without `--yes` before any
controller call and refuses `#space`. It resolves NAME once to the immutable
channel ID in one catalog read and sends the finite typed `chat.channel.delete`:
room ID, channel ID, desired generation and `confirm: true`, with no other
field. The operation registry and operator method list admit it like the other
channel mutations: box-allowed, mutating and effect-gated.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Before saving, the controller requires the running server's
`channel-add-reload` and, for deletion, `channel-delete-reload`. A missing
feature is "upgrade required". It also requires the applied status to match the
current desired render. A create after a deletion whose reload was not proven
is refused as pending until explicit ensure applies it. Deletion then removes
the non-default channel and appends `{id, name, generation}` to
`deleted_channels`. That ledger is identity only: no content, no access, no
reserved name. It is bounded to 256 entries, a full ledger refuses before any
write, and IDs are unique and disjoint from active ones. Names are canonical and
never `#space`; generations are positive, ordered and not newer than the room.
A recreated name gets a fresh ID. Then one publication, one reload and the
acknowledgment/status proof follow, as for other channel mutations; a lost
reply stays uncertain and nothing is retried.
[4](#source-4) [5](#source-5) [6](#source-6)

[FACT] The server configuration renders `removed_channels`: ledger names absent
from the active catalog, sorted and unique, and omitted when empty, so rooms
without deletions keep their bytes. The human stream header and catalog reads
carry the ledger; participant `identity.json` files do not. Independent import
remaps ledger IDs and rebases their generation to the restored room's first.
[6](#source-6) [7](#source-7) [8](#source-8) [4](#source-4)

[FACT] Each human stream freezes its original generation and channel IDs. The
relay refuses a channel-bearing request whose name was deleted after that
generation, or whose current ID differs from the original, even when a new
channel reuses the name: open a new chat connection. The driver mirrors this
before Write. A catalog may drop a channel only with its matching record, and
the ledger only grows. Names deleted after the connection opened report
membership `deleted`; their sends, controls and history are refused locally,
and queued setup/history is dropped. A pending send keeps its genuine server
outcome. Any self-KICK while an action is pending on that channel (deletion
before or after its catalog, or an ACL change) finishes the action once as
uncertain, never replayed; the stream and other channels continue.
[9](#source-9) [10](#source-10) [11](#source-11) [12](#source-12)

[FACT] The chat view keeps a deleted channel's history, draft and reply
read-only until chat closes, also after later reports, a catalog listing the
name again, or an in-view reconnect. Sending, join/part/topic and older pages
are refused locally, and the channel list and tab bar mark it deleted. Each
view is bound to the channel ID its first catalog named. A later catalog that
records that ID's deletion, or names another ID for the name, makes it deleted
even when the deletion happened while the chat was disconnected. Stream
messages, rosters and topics for a deleted view's name are not shown there.
[13](#source-13) [14](#source-14) [20](#source-20)

[FACT] Regressions cover the CLI and request strictness, the feature gate with
zero effects, one confirmed reload, a lost acknowledgment without retry, the
create gate, relay refusal across delete/recreate, the ledger, rendering,
import, driver catalog/refusal/pending outcomes and the UI view. The opt-in
real-AIRC test also deletes with a `channel-delete-reload` candidate: KICK,
restart persistence and a clean same-name channel. It keeps only the old
refusal on older servers. None of these proves a VM or live room.
[15](#source-15) [16](#source-16) [17](#source-17) [18](#source-18) [19](#source-19)

## Sources

1. <a id="source-1"></a> [CLI](../../cmd/yoloctl/chat_channels.go#L70-L131) — `delete` command, `channelRequestFromSnapshot`, `runChatChannelMutation`.
2. <a id="source-2"></a> [Typed request](../../cmd/yoloctl/chat_channels_runtime.go#L22-L74) — `decodeChatChannelRequest`.
3. <a id="source-3"></a> [Registry](../../cmd/yoloctl/controller_internal.go#L42) — `chat.channel.delete`; `operatorChatMethod` in chat_operator.go.
4. <a id="source-4"></a> [Ledger](../../internal/managedchat/channels.go#L14-L196) — `DeletedChannel`, `ValidateDeletedChannels`, `RemovedChannelNames`, `reidentifyChannels`.
5. <a id="source-5"></a> [Mutation](../../internal/managedchat/channels.go#L266-L295) — `DeleteChannel`.
6. <a id="source-6"></a> [Apply](../../cmd/yoloctl/chat_channels_runtime.go#L197-L262) — `mutateChatChannels`, `applyChatChannelProof`.
7. <a id="source-7"></a> [Rendering](../../internal/managedchat/render.go#L76-L156) — `removed_channels`, `Bootstrap.DeletedChannels`; filled at L288.
8. <a id="source-8"></a> [Stream header](../../internal/chatstream/protocol.go#L36-L64) — `RoomHeader`; catalog reads in `chatChannelCatalog` (chat_channels.go L255).
9. <a id="source-9"></a> [Relay](../../cmd/yoloctl/chat_stream.go#L85-L164) — `chatStreamOrigin`, `authorizeChatStreamChannel`.
10. <a id="source-10"></a> [Catalog](../../internal/chatstream/channel_catalog.go#L30-L174) — header ledger validation, `refreshCatalog`, `markDeleted`.
11. <a id="source-11"></a> [Driver](../../internal/chatstream/channels_client.go#L14-L234) — `deleted`, `origin`, `send`; history refusal in channel_history.go.
12. <a id="source-12"></a> [Controls](../../internal/chatstream/channel_events.go#L30-L310) — deleted refusals; `membershipEvent` finishes a pending action uncertain on a self-KICK.
13. <a id="source-13"></a> [View](../../internal/chatui/channels.go#L50-L522) — `deleted`, `canSend`, `membershipChanged`, `control`.
14. <a id="source-14"></a> [Composer](../../internal/chatui/model.go#L1217-L1265) — read-only send; paging in `historyPage` at L567.
15. <a id="source-15"></a> [Controller regressions](../../cmd/yoloctl/chat_channel_delete_test.go#L1-L286) — request/CLI, feature gate, one reload, lost acknowledgment, create gate, relay, view catalog identity.
16. <a id="source-16"></a> [Native regressions](../../internal/managedchat/channel_delete_test.go#L1-L197) — mutation, ledger bounds, rendering and import.
17. <a id="source-17"></a> [Driver regressions](../../internal/chatstream/channel_delete_test.go#L1-L281) — explicit removal, refusals before Write, pending outcomes, header records, KICK before/after the catalog.
18. <a id="source-18"></a> [View regression](../../internal/chatui/channel_deleted_test.go#L1-L122) — read-only deleted view, including a deletion missed while disconnected.
19. <a id="source-19"></a> [Real AIRC](../../internal/managedchat/airc_channel_integration_test.go#L349-L420) — opt-in deletion, restart and recreation; skipped on older servers.
20. <a id="source-20"></a> [Host catalog](../../cmd/yoloctl/chat_view.go#L817-L834) — `chatCatalog` passes channel IDs and ledger IDs; view binding in `bindIdentities` (chatui/channels.go).
