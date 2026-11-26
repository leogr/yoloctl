# Remedies for sessions of another build

Verification: `3736b1cf9ac349827782dcbe01070d3e5d922a15` and accompanying changes; re-read cited sources when changing behavior.

[FACT] A view's admission is classified like the stats handshake. The holder's
exact `session build/core mismatch` refusal, alone or followed by `; ` and the
advice its build appends, and a well-formed one-byte HELLO of another protocol
match `session.ErrBuildMismatch` while keeping their own messages. A malformed
HELLO keeps the protocol mismatch text without matching; a look-alike or any
other refusal is unchanged. The direct attach and the controller's relay of a
managed view both apply this: the relay forwards the frames as before and names
a foreign HELLO itself, since the view side refuses it.
[1](#source-1) [5](#source-5)

[FACT] `attachToSession` replaces a mismatch from either path with the remedy a
full drift resolution proves for the declared box, through the same batch read
as `box.drift`; any other outcome passes through unchanged. Recreation drift
names its reason, `yoloctl box inspect NAME --space SPACE`, then an explicit
`yoloctl box recreate NAME --space SPACE --no-attach` and attaching again, and
says that recreation ends the agent's current process and replaces its
container while the workdir and the account's native state are kept. In-sync,
restart-only and live-only drift name `yoloctl box restart NAME --space SPACE
--no-attach`. A failed resolution or unknown or error drift states that neither
could be determined and names `box inspect` only. Agent boxes add the same
conversation note as the post-update offer; restart and recreation both keep
the conversation record. The error still matches `ErrBuildMismatch` and is not
a user error. Nothing is restarted or recreated. [2](#source-2) [3](#source-3)

[FACT] `box.drift` is a read-only, host-admin controller view for 1 to 64
distinct valid box names. One `ResolveNamed` call at `FullDiff` lists the
space once and inspects, composes and probes only those boxes, within one
partial-data minute for the view, its upgrade form included; the caller's
cancellation still ends it. The upgrade form reads each box's upgrade target
as soon as that box's drift is resolved, through the resolver's per-box `Each`
hook; the restart offer reads no target. Each row carries its drift and
reasons and a drift error is that box's. Rows determined within the minute keep their result; a
box not determined by then reports its drift error, else the expiry, as its
error, and a failed batch is every row's error. Operators are refused, and
periodic inventories never use it. [2](#source-2) [4](#source-4) [7](#source-7)

[FACT] After a controller update, the restart offer reads `box.drift` for its
stale boxes only. A box that also requires recreation is set aside before the
listing and prompt, left running, and named with its reason, the inspect and
explicit recreate commands, and its recovery step: `chat ensure` for the room's
server, `box attach` for another workload, otherwise its conversation note.
Neither the prompt nor `--restart-sessions` restarts or recreates it. A box whose
drift is not reported or not determined stays in the offer with a note, and a
failed read keeps every box with a warning; each restart still refuses
recreation drift before its teardown. [3](#source-3) [6](#source-6)

[FACT] Before a fresh session in an existing container, `guardFreshSessionImage`
uses the shared full resolver under its caller's context. In controller-owned
attach setup, which can outlive its viewer while it holds the native space lock
and, for a tracked box, the box lock, one two-minute fail-safe ends a silent
resolution; its evidence is then unavailable and the guard refuses without a
launch. Independently recorded
image drift in `DiffPlan.ImageRecreateReason` refuses before conversation record
changes, holder launch or npm maintenance, with explicit space-qualified inspect
and recreate commands and the container-private-file loss warning. A live holder
keeps its existing fast attachment path. Inspection failures remain unknown and
refuse a fresh launch without asserting recreation. A normal exit 137 remains
its original exit status; this guard establishes image drift, not the mechanism
behind a killed process. [8](#source-8) [9](#source-9) [10](#source-10)

[FACT] Recreate-only drift is refused before image work: `box start` and
`box restart` of an existing container and an attach of a stopped box call
`refuseRecreateBeforeImageWork` (shared resolver within its own 30 s bound;
unreadable evidence decides nothing, so giving up fails no command) before warming, with the start/restart refusal text; a running
container without a holder runs `guardFreshSessionImage` before its effect gate;
the chat server's own restart/recreate rule runs in `warmChatServer` before its
warm. The later checks stay. Interactive `box start`/`restart`/`attach` and
`chat`/`chat ensure` first read `box inspect --json`; only a `recreate` strategy
(or `restart` for the room's server) prompts once, and a yes sends
`box recreate --force --no-attach` and then the original request unchanged. A
running box's start or attach, `--no-attach`, structured use and chat-guarded
attaches are never offered. [11](#source-11)

[FACT] Regressions cover the classification through a fake holder and the
relay, both attach paths end to end with an older protocol, every remedy and
pass-through, the bounded host-only view, a real resolver batch whose
inspection never answers (one listing, work only for the named boxes, ended by
the view's minute), both views keeping resolved rows past the minute, the
guidance and ordinary guard under their caller's deadline and cancellation, the
owned-setup guard refusing at its fail-safe without launch work, and the offer's script, prompt, flag, recreation-only,
unavailable, unknown and workload cases. They use fakes; no
live update was run. [5](#source-5) [6](#source-6)

## Sources

1. <a id="source-1"></a> [View refusals](../../internal/session/view.go#L84-L180) — `Relay` first-frame check, `protocolMismatch`, `foreignHello`, `viewRefusal`; `attachView` at L194 and L288; `buildMismatch` and `holderMismatchReason` in stats.go.
2. <a id="source-2"></a> [Remedies](../../cmd/yoloctl/session_remedy.go#L1-L155) — `sessionRemedyOf`, `staleSessionInspectTimeout`, `resolveStaleSessionsFn`, `staleSessionError`, `staleSessionGuidance`, `conversationNote`, `staleSessionMessage`, `boxDriftMethod`, `collectBoxDrift`; callers in session_client.go `attachToSession` L121 and L207.
3. <a id="source-3"></a> [Restart offer](../../cmd/yoloctl/controller_sessions.go#L250-L391) — `setAsideRecreations`; the `drift` closure L75, `restartConversationNote`.
4. <a id="source-4"></a> [Controller view](../../cmd/yoloctl/controller_views.go#L286-L298) — `box.drift` in `runControllerView`; its registry entry in controller_internal.go L77.
5. <a id="source-5"></a> [Classification regressions](../../internal/session/view_mismatch_test.go#L18) — `TestViewRefusalsClassifyOnlyExplicitBuildAnswers`.
6. <a id="source-6"></a> [Remedy regressions](../../cmd/yoloctl/session_remedy_test.go#L1) — attach paths, guidance cases, `box.drift`, `TestBoxDriftBatchHasOneInventoryAndOneBound`, the offer; caller, partial-minute and owned-setup fail-safe regressions in session_remedy_budget_test.go.
7. <a id="source-7"></a> [Named resolution](../../internal/boxstate/boxstate.go#L256-L472) — `ResolveNamed` and `resolveSpace`; spy regression `TestResolveNamedLimitsPerBoxWorkToTheNamedBoxes` in boxstate_test.go L245; the partial read `incompleteAtDeadline`, `TestResolveNamedIsPartialWhenItsContextEnds` in partial_test.go.

8. <a id="source-8"></a> [Fresh-session guard](../../cmd/yoloctl/session_remedy.go#L271) — `guardFreshSessionImage`; caller in session_client.go before `beginTrackedSession`.
9. <a id="source-9"></a> [Shared diff](../../internal/agentbox/diff.go#L179) — `Diff`, `imageRecreateReason`, `ImageRecreateReason`.
10. <a id="source-10"></a> [Guard regressions](../../cmd/yoloctl/session_image_test.go#L1) — isolated resolver, live-holder and preserved exit status.
11. <a id="source-11"></a> [Early recreation](../../cmd/yoloctl/recreate_offer.go#L50-L111) — `offerRecreate`, `boxRecreateOffer`; `refuseRecreateBeforeImageWork` in session_remedy.go; callers in agentbox.go (start, restart), session_client.go (`ensureAgentSession`), chat_runtime.go (`warmChatServer`), controller_client.go (`callControllerTerminal`), chat_open.go and chat.go; regressions in recreate_offer_test.go (main `3736b1c` and accompanying changes).
