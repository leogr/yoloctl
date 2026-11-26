# Managed chat runtime

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; output and recreate-offer claims re-read at `eddfb42a14f31c9f8d85069f2a861931367da86d`; re-read cited symbols when changing behavior. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes. Chat wait lifetimes re-read at `df8bb23207bf44b3c42ac46aa4d023f59049f207` and accompanying changes.

Mounts, rendering, roles and the applied-status proof are in
[membership application](chat-application.md); the confined human stream, its
failures and away/back are in [human stream](chat-stream.md).

[FACT] Host-only `chat` inspects before opening; it ensures only absent,
unprepared or provably stopped servers, including recreation of a deleted
reserved server declaration. Unavailable proof requires explicit
inspection, never automatic recovery. Status/catalog read strict atomic native
snapshots without mutation locks. An optional runtime read gets one minute;
expiry leaves runtime evidence pending while preserving verified server
proof and the native roster, including enrolled and revoked member IDs. Caller cancellation and changed snapshots
fail the read. [8](#source-8) [9](#source-9) Finite
ensure/status/enroll/revoke also permit verified same-space operators. All need
a matching running controller; none boots a VM or adopts unrelated boxes. First
creation defaults to box `chat`, channel `#space` and the host username;
operators must supply `--human`. Existing human identity persists. Named-client
connection is separate from membership. [7](#source-7) Typed mutations retain
admission/receipts and reject partial inventories. Waiting for a newly started
server's control socket has a two-minute fail-safe. Silent status attempts can
expire and retry; invalid peer or status proof is refused.
[14](#source-14) Lifecycle/build diagnostics
use stderr; stdout carries only the final result: JSON with `--json`, otherwise
plain text (`writeChatStatus`). Failures produce no success or replay.
[1](#source-1) [2](#source-2) [5](#source-5) [6](#source-6)

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
server box's exact `box diff` and `box recreate … --space` commands). On a
terminal, `chat ensure` without `--json` first reads `chat.status` and offers to
recreate the room's own server box, sending that recreate on yes
(`offerChatServerRecreate`); `yoloctl chat` makes the same offer only when it is
about to run `chat.ensure` (server not ready), so with a ready server pending
recreation it opens without one. No other chat command recreates it. `chat status` sets
`server.recreate_pending` and `server.reason` beside an otherwise ready server.
No value appears in a reason. [2](#source-2) [3](#source-3) [4](#source-4) [10](#source-10) [11](#source-11)

[FACT] Native creation retains controller-derived chat discovery beside private
account overrides in the sealed child bootstrap; account values stay out of
inspected container ENV, argv and labels. Account overrides still replace
arbitrary image/literal envelopes; reserved chat-key collisions refuse before
container creation. Without private injection, the existing inspected-envelope
selection and server temporary-directory checks remain unchanged. [12](#source-12)

[FACT] On the upgrade integration tree, chat server/member warming before
effects runs under the same native creation context that ordinary creation
uses (`defaultCreationContext`), so a cached server/member image is retained
through the native image lease before any effect; the original post-apply
refusal and no-replay rules are unchanged. [13](#source-13)

## Sources

1. <a id="source-1"></a> [CLI](../../cmd/yoloctl/chat.go#L18) — `newChatCmd`, `callChatController`, `writeChatStatus`; chat_open.go `offerChatServerRecreate`, `openChatHeader`.
2. <a id="source-2"></a> [Runtime service](../../cmd/yoloctl/chat_runtime.go#L39) — dispatcher, `ensureManagedRoom`, `ensureManagedServer`, membership apply/recreate.
3. <a id="source-3"></a> [Box projection](../../internal/agentbox/chat.go#L15) — creation, discovery, drift and live verification.

4. <a id="source-4"></a> [Observation](../../cmd/yoloctl/chat_status.go#L15) — `observeManagedChat`.

5. <a id="source-5"></a> [Lifecycle output](../../cmd/yoloctl/chat_runtime_output_test.go#L22) — create/start, member recreation, build, preflight image removal and failure; VM subprocesses are stubbed.
6. <a id="source-6"></a> [Actual ensure wiring](../../cmd/yoloctl/chat_runtime_output_test.go#L110) — absent/stopped server paths stop at the native-only feature probe after stubbed lifecycle work.

7. <a id="source-7"></a> [Operator chat](operator-chat.md) — explicit authority, self-enrollment and transient participant connections.
8. <a id="source-8"></a> [Observation](../../cmd/yoloctl/chat_observation.go#L28-L139) — bounded snapshot reads.
9. <a id="source-9"></a> [Open](../../cmd/yoloctl/chat_open.go#L14) — inspect-first decision.

10. <a id="source-10"></a> [Server temporary directory](../../internal/agentbox/chat.go#L152-L299) — `chatServerEnvironment`, `addChatEnvironment`, `chatServerRuntimeReason`, `ChatServerDeploymentDrift`, `chatServerEnvironmentReason`; `recreateReason` in diff.go L224; ensure's refusal `managedServerPlanError` in chat_runtime.go L442; `ServerStatus` in managedchat/status.go L17-L28, set in chat_status.go L60 and printed by chat.go `writeChatStatus` L159.
11. <a id="source-11"></a> [Temporary directory regressions](../../internal/agentbox/chat_env_test.go#L86-L209) — generated maps, conflicts, envelope cases, Diff and the start preflight, the unchanged literal; native `TestChatServerTemporaryDirectoryIsDeploymentDrift` in cmd/yoloctl/chat_server_tmpdir_test.go: an older server verifies, connects and shows status drift, ensure's decision refuses it, and explicit recreation replaces it.

12. <a id="source-12"></a> [Native chat environment](../../internal/agentbox/openshell_chat_environment_linux.go#L10-L45) — `retainChatEnvironment`; called after chat option assembly in `openshell_create_linux.go`. `openshell_chat_environment_linux_test.go` covers actual plan/bootstrap, account secrecy, reserved collisions and unchanged no-injection/server selection.
13. <a id="source-13"></a> [Native chat warming](../../cmd/yoloctl/chat_runtime.go#L152) — `runControllerChatRuntime` and `warmChatServer` call `warmBoxBeforeEffects` with `defaultCreationContext`; `TestNativeChatPrewarmRetainsCachedImageBeforeApply` in cmd/yoloctl/chat_native_warm_test.go (integration tree `ea2671d0`).
14. <a id="source-14"></a> [Server socket readiness](../../cmd/yoloctl/chat_runtime.go#L659-L684) — `waitManagedServer`.
