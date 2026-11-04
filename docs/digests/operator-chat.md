# Operator managed-chat administration

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] The server explicitly allows verified same-space operators to use
`chat.ensure`, `.status`, `.enroll`, `.revoke`, `.connect` and `.disconnect`.
Human `chat.stream`, nickname/view/attachment resolution, desired-only recovery
and controller receipt acknowledgment remain outside that authority. Public
commands use the shared typed transport; no chat prefix grants future methods.
The worker carries the listener's trusted principal, and chat services recheck
the effective grant, running container, incarnation and RO operator endpoint
after acquiring the native space lock. Mutations retain admission and durable
receipts, with no automatic replay. [1](#source-1) [2](#source-2) [11](#source-11)

[FACT] First operator setup needs explicit `--human NAME`; the guest username
cannot become the human identity by default. Existing rooms retain their human.
Enrollment/revocation use the existing native membership, rendering and exact
server application paths. Self `--recreate`, including a revoke naming the
caller's box or its member ID, is rejected before any write; a member-ID-shaped
argument always means that ID, so it is self only when it resolves to the
caller's membership, even if the caller's box name is the same string;
plain self-enrollment returns membership with pending binds. A peer's independent
managed session can explicitly recreate it afterward; a synchronous command
chain rooted in the replaced caller does not preserve its reply transport.
[2](#source-2) [3](#source-3) [9](#source-9)

[FACT] `chat enroll BOX --connect [--client space] [--recreate]` validates client
options before enrollment, then calls the existing typed connect only after an
acknowledged active membership. Failed/uncertain enrollment never connects;
connection failure retains membership and reports partial success without
retry. `--client` requires `--connect`; omitted `--connect` preserves enrollment
output. The shortcut emits one JSON result with enrollment plus connection or
connection error. Agent wake listening remains separate. [19](#source-19)

[FACT] Connect/disconnect is distinct from durable membership. It selects one
active immutable member and matching running declaration/container, verifies
its current private chat binds, then invokes fixed packaged AIRC argv through
confined SSH. Connect additionally requires exact current server proof and its
live holder. Disconnect does not need a ready server and stops only the chosen
named client. Requests accept a box and bounded client name, never config/state
paths or credentials. `--` keeps a leading-hyphen client name positional. AIRC
uses its default box-local state; no message, listener/wake harness, lifecycle
start, automatic reconnect or ACL change is implied. Bounded diagnostics use
stderr and stdout contains one acknowledged action. [4](#source-4)

[FACT] Arbitrary operator execution and terminal attachment cannot target the
reserved managed server, including a dormant reservation or missing/reused
declaration. Inspected stale server-role labels or human/config/data/control
binds also protect a target when room metadata is absent; unfiltered runtime
inventory includes targets missing the primary box label. Unreadable room
metadata or uncertain target runtime refuses execution. Ordinary participant
credential/endpoint binds do not carry human authority. Internal fixed chat
service commands remain available. [5](#source-5)

[FACT] Operator exec acquires native setup locking; operator attachment skips
the unlocked live-holder shortcut. Execution/attachment guards run before
session revival and actual socket selection, and setup is released only after
the target socket is connected. Native socket selection, including human
attachments, has a ten-second bound independent of the subsequent stream
lifetime. Release failures close selected attachment/exec transports before
streaming; exec setup does not synchronize roles. Finite lifecycle suppresses
incidental server attachment with a diagnostic; explicit exec/attach/box-use/run
still rejects.
Reserved declaration validation precedes a forced image build. Existing exact
workload validation and copy's omitted ID continue to protect projection.
[6](#source-6) [7](#source-7) [8](#source-8) [12](#source-12)
[13](#source-13) [14](#source-14) [15](#source-15)

[FACT] Focused regressions exercise the allowlist, foreign scope, human/default
identity, self-recreation preflight, stale participant identities, fixed argv,
bounded output/failure without replay, protected binds, explicit versus
incidental attachment, unlabelled runtimes, bounded setup and release before
streaming. Fixtures substitute runtime calls; they establish no live VM,
confinement or AIRC connection proof.
[10](#source-10) [16](#source-16) [17](#source-17) [18](#source-18)
[20](#source-20)

## Sources

1. <a id="source-1"></a> [Registry and worker](../../cmd/yoloctl/controller_internal.go#L36) — `controllerOperations`, `runControllerWorker`.
2. <a id="source-2"></a> [Operator preflight](../../cmd/yoloctl/chat_operator.go#L43) — `checkOperatorChatRuntime`, `validateOperatorChatRuntime`.
3. <a id="source-3"></a> [Runtime membership](../../cmd/yoloctl/chat_runtime.go#L39) — `runControllerChatRuntime`, enrollment/apply/recreate.
4. <a id="source-4"></a> [Participation](../../cmd/yoloctl/chat_participation.go#L84) — dispatcher, `resolveChatParticipant`, server proof and `executeChatParticipation`.
5. <a id="source-5"></a> [Protected target](../../cmd/yoloctl/chat_operator_server.go#L32) — `operatorManagedServerTarget`, `protectedManagedServerTarget`.
6. <a id="source-6"></a> [Native admission](../../cmd/yoloctl/space_operation.go#L30) — `registerSpaceOperations`.
7. <a id="source-7"></a> [Attachment](../../cmd/yoloctl/session_client.go#L47) — `prepareAutoAttach`, `selectSessionTransport`, `ensureAndAttachSession`.
8. <a id="source-8"></a> [Rebuild](../../cmd/yoloctl/agentbox_reconcile.go#L162) — `runRebuild`.
9. <a id="source-9"></a> [Installed guidance](../../assets/embed.go#L13) — `BoxOperatorSkill` embeds the managed chat workflow and independent peer-session rule.
10. <a id="source-10"></a> [Authority tests](../../cmd/yoloctl/chat_operator_test.go#L18) — allowlist, scope, first human identity and self-recreation preflight.
11. <a id="source-11"></a> [Typed frontend](../../cmd/yoloctl/chat.go#L75) — `callChatControllerInto`.
12. <a id="source-12"></a> [Exec](../../cmd/yoloctl/run.go#L95) — `execInAgentboxInteractive`.
13. <a id="source-13"></a> [Finite tail](../../cmd/yoloctl/agentbox.go#L247) — `maybeAutoAttach`.
14. <a id="source-14"></a> [Server declaration](../../internal/agentbox/chat.go#L53) — `ValidateChatServer`.
15. <a id="source-15"></a> [Copy](../../cmd/yoloctl/agentbox_copy.go#L190) — `copyOptionsFromSource`.
16. <a id="source-16"></a> [Participation tests](../../cmd/yoloctl/chat_participation_test.go#L28) — identity, fixed argv, bounded output and no replay.
17. <a id="source-17"></a> [Server guard tests](../../cmd/yoloctl/chat_operator_server_test.go#L18) — protected targets, attachment intent and selection ordering.
18. <a id="source-18"></a> [Combined identity and target selection](../../cmd/yoloctl/chat_attach_test.go#L87) — `TestChatAttachGuardedSelectionPinsValidatedIdentity`.
19. <a id="source-19"></a> [Enrollment shortcut](../../cmd/yoloctl/chat_enroll_connect.go#L24) — `runChatEnrollCommand`, active acknowledgment, separate receipts and partial JSON.
20. <a id="source-20"></a> [Shortcut CLI regressions](../../cmd/yoloctl/chat_enroll_connect_test.go#L49) — validation before mutation, sequencing, uncertain outcomes, partial success, operator routing and plain output.
