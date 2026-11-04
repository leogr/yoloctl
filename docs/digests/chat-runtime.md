# Managed chat runtime and human stream

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] Host-only `chat` inspects before opening; it ensures only absent,
unprepared or provably stopped servers, including recreation of a deleted
reserved server declaration. Unavailable proof requires explicit
inspection, never automatic recovery. Status/catalog read strict atomic native
snapshots without mutation locks; bounded unavailable runtime evidence remains
pending. Changed snapshots fail closed. [25](#source-25) [26](#source-26) Finite
ensure/status/enroll/revoke also permit verified same-space operators. All need
a matching running controller; none boots a VM or adopts unrelated boxes. First
creation defaults to box `chat`, channel `#space` and the host username;
operators must supply `--human`. Existing human identity persists. Named-client
connection is separate from membership. [24](#source-24) Typed mutations retain
admission/receipts and reject partial inventories. Lifecycle/build diagnostics
use stderr; stdout contains final JSON only. Failures produce no success or replay.
[1](#source-1) [2](#source-2) [21](#source-21) [22](#source-22)

[FACT] Runtime opt-in is durable; dormant rooms acquire no ordinary-box binds.
The confined agentless server uses the approved image, `yolo.chat.skill`,
ephemeral workdir, no ports/grants and fixed argv with umask 077. Native data
survives replacement. Ensure verifies its reserved declaration, labels and binds;
shared validation rejects substituted workloads before save, teardown or
projection. Drift requires explicit recreation. Enrollment adds the client
feature and immutable declaration ID. Existing participant binds change only
through explicit recreation. [2](#source-2) [3](#source-3)

[FACT] Through that child-environment channel the server's children, not
members', also get `TMPDIR` and `SQLITE_TMPDIR` set to `/tmp`; a conflicting
declared or injected value fails creation without naming it. The channel is read
at container creation, so an older server lacks them until an explicit
`box recreate`. Runtime verification, which gates connections, uploads, roles
and status, checks identity and binds only, so such a server keeps serving.
Full inspection reads the actual envelope (exactly one JSON object of strings
holding both values; an uninspected environment is unverified, never healthy)
and requires recreation: `box diff`/`show`/`inspect` and `box.drift` report it,
the start/restart preflight and `chat ensure` refuse (the latter naming the
server box's exact `box diff` and `box recreate … --space` commands; it never
recreates), and `chat status` sets
`server.recreate_pending` and `server.reason` beside an otherwise ready server.
No value appears in a reason. [2](#source-2) [3](#source-3) [7](#source-7) [35](#source-35) [36](#source-36)

[FACT] Native creation retains controller-derived chat discovery beside private
account overrides in the sealed child bootstrap; account values stay out of
inspected container ENV, argv and labels. Account overrides still replace
arbitrary image/literal envelopes; reserved chat-key collisions refuse before
container creation. Without private injection, the existing inspected-envelope
selection and server temporary-directory checks remain unchanged. [40](#source-40)

[FACT] Server mounts are config RO, data RW, human leaf RO, endpoint RW and
control RW. Participants receive their own credential leaf and endpoint RO.
Control stays server-only under `/run/yoloctl/chat-control`; VM endpoint parents
use `/run/yoloctl/controller/{chat,chat-control}/<room-id>`. Directory binds
preserve atomic replacement. Verification rejects foreign, duplicate, writable
or overlapping binds. Discovery uses the confined child environment channel;
credentials remain 0600 files. [3](#source-3)
[4](#source-4)

[FACT] Rendering verifies desired state, ownership and collision-free immutable
account encodings. Nicknames use enrollment order, human first, with immutable
collision fallbacks. Private `identity.json` maps authenticated identities;
nicknames confer no authority. Client files publish before server TOML. Revoked
recovery leaves remain; server ACL denial and client disconnection enforce
revocation. [5](#source-5)

[FACT] Controller-derived operator roles, persistent human nicknames and
identity-guarded participant attachment use the existing native lifecycle;
see [roles, settings and attachment](chat-settings.md) for their proof and
explicit reconnect/start boundaries. Role synchronization retains published
changes as pending when the holder is stopped and control is absent/refused,
even inside a running container; verified live orphans still reload once.
Explicit ensure owns startup. [23](#source-23)

[FACT] Applied status requires exact generation/SHA256/account count, Unix IRC
and artifact listeners, no revoked connections, a live holder and verified
container binds. SO_PEERCRED plus the container's cgroup in the guest's
workload pool (systemd slice or microvm group) binds control responses to
runtime. Control allows one Unix request per connection, 16 KiB requests and
1 MiB responses, without reload replay. Failed application retains desired
state as pending; `server_ready` differs from complete `runtime_applied`.
Feature probes require client/server presence support. Reload is followed by
fresh status proof; no TCP fallback exists.
[2](#source-2) [6](#source-6) [7](#source-7) [12](#source-12) [19](#source-19)

[FACT] `chat.stream` pins one confined SSH peer, verifies exact container,
declaration (excluding its runtime chat projection), binds and applied
incarnation around probing, then runs a fixed launcher: it copies the published
human client config, setting the nickname and, only in take-over mode,
`regain = true`, into an unlinked, owner-only temporary descriptor and execs
`airc --jsonl 2` on it. Without filesystem `O_TMPFILE` support, exclusive
creation and unlink precede writing any config.
Each stream registers the saved nickname, and one human session holds it. If
the verified server status counts a live human-account connection, a stream
without the `take-over` flag writes only `{"event":"nick-held"}` and fails
before the effect gate, so the receipt never becomes uncertain. The count is an
observation, not a reservation: a session that just closed can still show the
prompt, and Take over then registers with nothing to close. The host asks
the user on `ErrNickHeld`, or on a raced client `NICK_HELD` failure; Esc
closes the view. Take-over uses AIRC's same-account REGAIN. The replaced view
ends with `ErrTakenOver`, from the ERROR record or the closed event's
`server_error`, and never reconnects by itself. Another account's holder fails
as 433, shown as a hard error, and nothing is retried. The stream requires the
client feature `nick-regain`.
[37](#source-37) [38](#source-38) Setup takes no mutation lock, acknowledges the
durable effect gate and releases admission; cancellation closes stalled SSH.
The OpenShell root supervisor owns the pinned Unix socket. [27](#source-27) [28](#source-28)
Closing the view closes only its client. The controller canonicalizes an
allowlist of room send/read/history/members/who/typing/quit and single-channel
join/part/topic operations: no argv, artifact paths or raw IRC. The channel path
requires `client-topic`; see [catalog and stream](chat-channels.md) for current
ACL checks, reload prerequisites and typed multi-channel driving. Host frames are bounded; reads use `next`, not
`latest`. A queued send is pending; complete correlated receipts confirm it.
Epoch changes, gaps, partial frames and disconnects remain explicit, with no
reconnect/resend. Clipboard/link callbacks require a user action and existing
host policy. [8](#source-8) [9](#source-9) [13](#source-13) [14](#source-14)

[FACT] Stream failures distinguish EOF, malformed frames, local deadlines and
transport errors. A failed worker's last 2 KiB of stderr accompanies its exit
error as sanitized single-line text; receipt and retry behavior is unchanged.
The input filter reads a pollable duplicate of inherited worker stdin, so an
early client exit can close and join it while the host keeps stdin open,
preserving the client error instead of waiting for the handshake deadline.
A filter refusal is published before the client sees its EOF, so a client
exiting cleanly on that EOF still reports the refusal.
[31](#source-31) [32](#source-32) [39](#source-39)

[FACT] Only the adapter's own encoding or allowlist, before it invokes the
stream writer, creates a typed local refusal. That send is `rejected`, its
pending slot clears and the same stream continues. After Write, established
definite peer refusals, a `not sent:` reply or a correlated rejected receipt,
remain `rejected` on a live stream. Transport, protocol and unproven peer
failures remain `uncertain` and end the stream: zero or partial bytes, lost,
malformed or uncorrelated replies, and peer errors that only repeat the local
check's text. Nothing is retried. Synthetic-peer regressions inject the former
attachment-capability mismatch into the real channel loop. [33](#source-33) [34](#source-34)

[FACT] Tests cover projection/ownership, failed writes, identity reuse, application
proof, frames/receipts and archive independence. Real kernel locks, subprocesses
and Unix/SSH fixtures cover read/open contention, cancellation and replacement;
runtime/cgroup inspection is stubbed. Opt-in real AIRC control tests also stub
peer verification. None proves VM authority/confinement, RO-bind connectivity,
SQLite quiescence or graphical fidelity; isolated host validation remains required.
[29](#source-29) [30](#source-30)
[10](#source-10) [11](#source-11) [15](#source-15)
[16](#source-16) [17](#source-17) [18](#source-18) [20](#source-20)

[FACT] On the upgrade integration tree, chat server/member warming before
effects runs under the same native creation context that ordinary creation
uses (`defaultCreationContext`), so a cached server/member image is retained
through the native image lease before any effect; the original post-apply
refusal and no-replay rules are unchanged. [41](#source-41)

## Sources

1. <a id="source-1"></a> [CLI](../../cmd/yoloctl/chat.go#L18) — `newChatCmd`, `callChatController`.
2. <a id="source-2"></a> [Runtime service](../../cmd/yoloctl/chat_runtime.go#L39) — dispatcher, `ensureManagedRoom`, `ensureManagedServer`, membership apply/recreate.
3. <a id="source-3"></a> [Box projection](../../internal/agentbox/chat.go#L15) — creation, discovery, drift and live verification.
4. <a id="source-4"></a> [Native binds](../../internal/managedchat/projection.go#L13) — `Projection`, `EnableRuntime`, `PrepareEndpoints`.
5. <a id="source-5"></a> [Renderer](../../internal/managedchat/render.go#L22) — accounts, nicknames, identity map, snapshot publication.
6. <a id="source-6"></a> [Control proof](../../internal/managedchat/control.go#L17) — `Applied`, `Control.Call`.
7. <a id="source-7"></a> [Observation](../../cmd/yoloctl/chat_status.go#L16) — `observeManagedChat`.
8. <a id="source-8"></a> [Controller stream](../../cmd/yoloctl/chat_stream.go#L18) — setup and relay.
9. <a id="source-9"></a> [JSONL adapter](../../internal/chatstream/client.go#L156) — `Run`, receipt/echo correlation.
10. <a id="source-10"></a> [Runtime regressions](../../cmd/yoloctl/chat_runtime_test.go#L16) — reserved ownership, partial inventory, name reuse and operator/human operation separation.
11. <a id="source-11"></a> [Stream regressions](../../internal/chatstream/client_test.go#L19) — receipts, spoofing, epochs, gaps and disconnects.
12. <a id="source-12"></a> [Linux peer](../../cmd/yoloctl/chat_peer_linux.go#L17) — `verifyChatControlPeer`, `chatPeerCollector`; `workloadPoolRel` in workload_pool.go.
13. <a id="source-13"></a> [Stream allowlist](../../internal/chatstream/protocol.go#L101) — `DecodeRequest`, `Filter`.
14. <a id="source-14"></a> [Host view](../../cmd/yoloctl/chat_view.go#L30) — `runChatView`.
15. <a id="source-15"></a> [Proof regressions](../../internal/managedchat/control_test.go#L17) — exact proof and rejected peer/bounded response tests.
16. <a id="source-16"></a> [Nickname regressions](../../internal/managedchat/nickname_test.go#L8) — case collisions and immutable fallbacks.
17. <a id="source-17"></a> [Request bounds](../../internal/chatstream/protocol_test.go#L10) — foreign authority and partial frame rejection.
18. <a id="source-18"></a> [Archive independence](../../internal/spacearchive/chat_test.go#L65) — fresh identities/credentials and retained synthetic data.
19. <a id="source-19"></a> [Listener wire contract](../../internal/managedchat/control_test.go#L119) — literal status JSON, Unix-only endpoints and rejected shapes.
20. <a id="source-20"></a> [Real control interoperability](../../internal/managedchat/airc_integration_test.go#L22) — `TestRealAIRCControlContract`; opt-in published server, stubbed local peer verification.
21. <a id="source-21"></a> [Lifecycle output](../../cmd/yoloctl/chat_runtime_output_test.go#L22) — create/start, member recreation, build, preflight image removal and failure; VM subprocesses are stubbed.
22. <a id="source-22"></a> [Actual ensure wiring](../../cmd/yoloctl/chat_runtime_output_test.go#L110) — absent/stopped server paths stop at the native-only feature probe after stubbed lifecycle work.
23. <a id="source-23"></a> [Settings and roles dispatcher](../../cmd/yoloctl/chat_runtime.go#L40) — admitted settings/role mutations and observational attachment lookup.
24. <a id="source-24"></a> [Operator chat](operator-chat.md) — explicit authority, self-enrollment and transient participant connections.
25. <a id="source-25"></a> [Observation](../../cmd/yoloctl/chat_observation.go#L28-L129) — bounded snapshot reads.
26. <a id="source-26"></a> [Open](../../cmd/yoloctl/chat_open.go#L14) — inspect-first decision.
27. <a id="source-27"></a> [Pinned connection](../../cmd/yoloctl/chat_connection.go#L44) — peer verification and cancellation.
28. <a id="source-28"></a> [OpenShell v0.0.72](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-process/src/ssh.rs#L53) — `ssh_server_init`.
29. <a id="source-29"></a> [Availability](../../cmd/yoloctl/chat_availability_test.go#L26) — actual handlers and lock contention.
30. <a id="source-30"></a> [Connection](../../cmd/yoloctl/chat_connection_test.go#L157) — real Unix/SSH fixtures; runtime authority stubbed.
31. <a id="source-31"></a> [Stream diagnostics](../../cmd/yoloctl/chat_stream_stderr.go#L20) — `callChatStream`, bounded `stderrTail`; real controller transport regression in `chat_stream_stderr_test.go`.
32. <a id="source-32"></a> [Frame failures](../../internal/chatstream/client.go#L287-L317) — `closeFor`, `frameError`; deadline and malformed-input regressions in `stream_diag_test.go`.
33. <a id="source-33"></a> [Pre-write refusal](../../internal/chatstream/client.go#L320-L411) — `localRejection`, `exchange`, `driver.send`.
34. <a id="source-34"></a> [Refusal regressions](../../internal/chatstream/local_rejection_test.go#L131-L295) — `TestChannelsPreWriteRejectionKeepsTheStream` runs `channelDriver.run` with a synthetic peer; `TestChannelsWrittenSendFailuresStayUncertain`.
35. <a id="source-35"></a> [Server temporary directory](../../internal/agentbox/chat.go#L152-L291) — `chatServerEnvironment`, `addChatEnvironment`, `chatServerRuntimeReason`, `ChatServerDeploymentDrift`, `chatServerEnvironmentReason`; `recreateReason` in diff.go L224; ensure's refusal `managedServerPlanError` in chat_runtime.go L442; `ServerStatus` in managedchat/status.go L17-L28, set in chat_status.go L60 and printed by chat.go `writeChatStatus` L159.
36. <a id="source-36"></a> [Temporary directory regressions](../../internal/agentbox/chat_env_test.go#L86-L209) — generated maps, conflicts, envelope cases, Diff and the start preflight, the unchanged literal; native `TestChatServerTemporaryDirectoryIsDeploymentDrift` in cmd/yoloctl/chat_server_tmpdir_test.go: an older server verifies, connects and shows status drift, ensure's decision refuses it, and explicit recreation replaces it.
37. <a id="source-37"></a> [Nickname take-over and launcher](../../internal/chatstream/connection.go#L11-L47) — `ErrNickHeld`, `ErrTakenOver`, `NickHeldEvent`, `TakeOverFlag`, `ClientArgv`; embedded client_config.py (`derive`, `main`, `TemporaryFile` unlinks before writes); `runControllerChatStream` refusal at chat_stream.go L53–L65 and `chatStreamTakeOver` L91–L100; `managedChatConnection.Connected` in chat_connection.go L166–L174; driver mapping in channels_client.go `run` L78, channel_events.go `record` L172 and client.go `exchange` L331–L370; host `chatSession.start`/`takeOver` in chat_view.go L266–L396 and chatui `nickHeld`/`overlayTakeOver`.
38. <a id="source-38"></a> [Take-over regressions](../../internal/chatstream/airc_views_integration_test.go#L14) — opt-in real-AIRC `TestRealAIRCHumanViewTakeOver` (NICK_HELD leaves the holder, take-over ends it as `ErrTakenOver` with peer QUIT, another account's 433); takeover_test.go (nick-held frame, ERROR and closed-event mapping); launcher tests in connection_test.go, including `TestClientLauncherEnablesRegainOnlyForTakeOver` and `TestClientLauncherWithoutOTmpfile`; `TestChatStreamUsesTheSavedNickAndRefusesAHeldOneBeforeEffects` in cmd/yoloctl/chat_stream_test.go; `TestNickHeldAsksToTakeOverOrClose` in internal/chatui/takeover_test.go.
39. <a id="source-39"></a> [Relay input cleanup](../../cmd/yoloctl/chat_stream.go#L89) — `relayManagedChatWithExec`, `chatFilterInput`; `TestChatRelayExitDoesNotWaitForStdin` in cmd/yoloctl/chat_stream_test.go keeps inherited blocking stdin open with empty or partial input while the client succeeds or fails; `TestChatRelayPreservesFilterFailure` retains a filter refusal; `TestChatRelayReportsRefusalBeforeClientEOF` covers a client that exits cleanly on the refusal's EOF.

40. <a id="source-40"></a> [Native chat environment](../../internal/agentbox/openshell_chat_environment_linux.go#L10-L45) — `retainChatEnvironment`; called after chat option assembly in `openshell_create_linux.go`. `openshell_chat_environment_linux_test.go` covers actual plan/bootstrap, account secrecy, reserved collisions and unchanged no-injection/server selection.
41. <a id="source-41"></a> [Native chat warming](../../cmd/yoloctl/chat_runtime.go#L152) — `runControllerChatRuntime` and `warmChatServer` call `warmBoxBeforeEffects` with `defaultCreationContext`; `TestNativeChatPrewarmRetainsCachedImageBeforeApply` in cmd/yoloctl/chat_native_warm_test.go (integration tree `ea2671d0`).
