# Managed chat roles, nicknames and attachment

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; native nickname entry points verified at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] An enrolled box receives configured IRC channel `+o` only when its
current declaration ID matches membership, its effective capabilities include
`yolo.space.operator`, and its running container has matching scope,
operator-incarnation/endpoint and exact managed-chat labels/binds. Shared
accounts and nicknames confer no authority. This is channel moderation, not
server OPER or controller authorization. Human channel ownership remains
configured independently. [1](#source-1) [2](#source-2)

[FACT] The native room stores a controller-derived `channel_operator` snapshot.
Changed roles advance generation, publish private configuration and reload the
verified server once, followed by exact generation/digest proof. Existing
box mutation guards, box editor application and chat membership/settings/ensure
own synchronization. Attachment setup propagates synchronization errors before
releasing admission for its stream. Exec selection, participation connections,
dry-run and status do not repair roles.
External runtime changes remain pending until a managed mutation/ensure
synchronizes them. Failed inventory or
inspection cannot grant authority. Failed application retains desired state and
reports pending recovery; unchanged roles do not automatically replay an earlier
uncertain reload. Missing/stopped servers retain published configuration for
explicit start. Identity/binds and authoritative service state precede control.
Stopped/absent holders get one verified status probe: only dial ENOENT/ECONNREFUSED
defer with a pending notice; live orphan servers reload once. Other errors fail.
Active/activating holders get bounded read-only startup probes. Reload
acknowledgment and fresh proof must match the readiness incarnation; no holder
starts. [1](#source-1) [23](#source-23) Container preparation and every fresh server-holder launch
refuse stale/unavailable published bytes after an interrupted publication,
including normal child exit inside a still-running container. Existing live
holders are reused without a new launch or implicit reload; explicit ensure
renders before recovery.
A desired snapshot is not proof of live MODE removal.
[1](#source-1) [3](#source-3) [4](#source-4) [13](#source-13)
[17](#source-17) [18](#source-18)

[FACT] Host-only `chat nickname NICK` uses admitted, receipted `chat.nickname`
with `nickname` and optional `generation`. The host native view's `/nick NAME`
and Settings › Nickname use that same operation, then explicitly reconnect
after a successful save. [27](#source-27) It changes the human's saved display
preference, preserving canonical name, room/member/account IDs and tokens.
Supported nicknames are 1–24 ASCII letters, digits, underscores and hyphens,
starting with a letter or underscore. Case-insensitive collisions with another
member's nickname or any immutable account are rejected. The next human stream
registers it [26](#source-26); existing clients keep the nickname they
connected with. The UI must explicitly save
and reconnect without replaying sends; no server/box restart or unsupported
JSONL NICK operation is introduced. Generated identity metadata includes the
chosen `nick` and enrolled `box_id`. Import preserves the nickname but clears
runtime-derived operator grants for the independent restored space.
[2](#source-2) [5](#source-5) [6](#source-6) [11](#source-11)
[12](#source-12) [14](#source-14)

[FACT] Host-admin `chat.attach-target` accepts only an immutable `member_id`
and never waits for the native space lock. Within 5 s it reads one strict
manifest snapshot and a complete runtime inventory, then re-reads the manifest:
a changed room, an unavailable inventory or the deadline is an error, never a
stopped box. It returns room/member/box IDs, state and a box name only when the
current declaration matches. Human, unknown, revoked and replaced identities
cannot select a box by display name. A matching declaration with absent runtime
has a nonempty box and `missing` state; an absent/replaced declaration has no
box target. The preview grants nothing; the guarded attach below rechecks under
the lock. [7](#source-7) [24](#source-24) [25](#source-25)

[FACT] Typed attachment carries all three IDs in hidden ordinary-attach guard
flags. The native mutation guard bypasses the unguarded live-holder shortcut,
rechecks membership and declaration identity, and verifies existing chat binds
before ordinary session lifecycle. `--chat-allow-start` records explicit start
consent; without it, a target that stopped after lookup is refused. These fields
constrain an already-authorized attachment and grant no new authority. UI tab
reuse must also perform fresh validation instead of dropping these guards.
Native socket selection remains under the identity guard's lock, has a bounded
setup context, and completes before release. A release error closes the selected
transport before streaming. [3](#source-3) [7](#source-7) [20](#source-20)

[FACT] Focused regressions exercise private nickname publication/recovery,
identity/token retention, role revocation/import, partial inventories, live-bind
verification and lookup/action name reuse. A combined guarded-attachment fixture
uses private native declarations and inert inspection to check selection before
release-induced name reuse. The opt-in AIRC control fixture
observes connected-peer `+o`/`-o` and account revocation over private Unix
listeners; its peer verifier is stubbed. The real channel fixture checks
human/operator MODE, NAMES and WHO in default/new channels, ordinary first
join, part/rejoin and connected role grant/revoke. [22](#source-22)
These checks do not establish VM
confinement, actual UID/cgroup verification, UI reconnection or tab routing.
[8](#source-8) [9](#source-9) [10](#source-10) [15](#source-15) [16](#source-16)
[19](#source-19)
[21](#source-21)

## Sources

1. <a id="source-1"></a> [Role synchronization](../../cmd/yoloctl/chat_roles.go#L28) — `desiredChatOperators`, `synchronizeChatRoles`, `applyChangedChatRoles`, `syncNativeChatRoles`.
2. <a id="source-2"></a> [Native settings](../../internal/managedchat/settings.go#L26) — `SetHumanNickname`, `SetOperators`.
3. <a id="source-3"></a> [Mutation guard](../../cmd/yoloctl/space_operation.go#L20) — `releaseSpaceOperation`, `registerSpaceOperations`.
4. <a id="source-4"></a> [Observation](../../cmd/yoloctl/chat_status.go#L16) — `observeManagedChat`.
5. <a id="source-5"></a> [CLI](../../cmd/yoloctl/chat.go#L18) — `newChatCmd`.
6. <a id="source-6"></a> [Trusted header](../../internal/chatstream/protocol.go#L33) — `RoomHeader`.
7. <a id="source-7"></a> [Attachment boundary](../../cmd/yoloctl/chat_attach.go#L17) — lookup, complete guard validation and action-time checks.
8. <a id="source-8"></a> [Settings regressions](../../internal/managedchat/settings_test.go#L17).
9. <a id="source-9"></a> [Role regressions](../../cmd/yoloctl/chat_roles_test.go#L27).
10. <a id="source-10"></a> [Real AIRC fixture](../../internal/managedchat/airc_integration_test.go#L22) — `TestRealAIRCControlContract`.
11. <a id="source-11"></a> [Renderer](../../internal/managedchat/render.go#L150) — private nick/role metadata and `VerifyPublished`.
12. <a id="source-12"></a> [Import](../../internal/managedchat/model.go#L152) — `Reidentify`.
13. <a id="source-13"></a> [Editor](../../cmd/yoloctl/controller_documents.go#L147) — deferred synchronization.
14. <a id="source-14"></a> [Dispatcher](../../cmd/yoloctl/chat_runtime.go#L40) — `runControllerChatRuntime`.
15. <a id="source-15"></a> [Header regression](../../internal/chatstream/settings_test.go#L9).
16. <a id="source-16"></a> [Attachment regressions](../../cmd/yoloctl/chat_attach_test.go#L25).
17. <a id="source-17"></a> [Server preparation](../../internal/agentbox/chat.go#L62) — shared `VerifyChatServerPublication` invariant.
18. <a id="source-18"></a> [Fresh holder launch](../../cmd/yoloctl/session_client.go#L413) — publication verification even when the container is running.
19. <a id="source-19"></a> [Holder revival regression](../../cmd/yoloctl/chat_session_publication_test.go#L27) — actual session path with isolated native files and inert service-command spies.
20. <a id="source-20"></a> [Locked transport selection](../../cmd/yoloctl/session_client.go#L75) — `selectSessionTransport`.
21. <a id="source-21"></a> [Release failure regressions](../../cmd/yoloctl/chat_operator_server_test.go#L332) — selection/attachment/exec close connected transports before streaming on release errors.
22. <a id="source-22"></a> [Real channel role fixture](../../internal/managedchat/airc_channel_integration_test.go#L108) — `TestRealAIRCChannelAdditionAndACLRemovalWithoutRevocation`; production rendering plus isolated aircd, stubbed peer verification.
23. <a id="source-23"></a> [Holder readiness regressions](../../cmd/yoloctl/chat_roles_readiness_test.go#L28) — lifecycle guard, real publication/private Unix control, inert runtime/service inspection, including live orphans.
24. <a id="source-24"></a> [Lock-free lookup](../../cmd/yoloctl/chat_attach_observe.go#L21-L55) — `observeChatAttachTarget`, dispatched before the lock in chat_runtime.go `runControllerChatRuntime`.
25. <a id="source-25"></a> [Lookup regressions](../../cmd/yoloctl/chat_attach_observe_test.go#L77) — held lock, deadline, unavailable inventory, room change, name reuse, revocation.
26. <a id="source-26"></a> [Stream nickname](../../cmd/yoloctl/chat_stream.go#L49-L65) — `runControllerChatStream` registers `RoomHeader`'s saved human nickname.
27. <a id="source-27"></a> [Native nickname save](../../cmd/yoloctl/chat_view.go#L89-L105) — `runChatView` wires `SaveNick` to `chat.nickname` and returns `NickSavedMsg`; chatui/model.go `slashCommand`/`nickSaved` and overlay.go `saveNick`/`activateSetting`; nickname_display_test.go `TestNicknameSaveReconnectRefreshesOpenLabels` exercises the component with fake callbacks, not the host/controller stream.
