# Remedies for sessions of another build

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

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
distinct valid box names. One `ResolveNamed` call at `FullDiff`, under one 30 s
bound for the whole request, lists the space once and inspects, composes and
probes only those boxes; each row carries its drift and reasons, a drift error
is that box's, and a failed batch is every row's error. Operators are refused,
and periodic inventories never use it. [2](#source-2) [4](#source-4) [7](#source-7)

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
uses the shared full resolver, bounded to 30 seconds. Independently recorded
image drift in `DiffPlan.ImageRecreateReason` refuses before conversation record
changes, holder launch or npm maintenance, with explicit space-qualified inspect
and recreate commands and the container-private-file loss warning. A live holder
keeps its existing fast attachment path. Inspection failures remain unknown and
refuse a fresh launch without asserting recreation. A normal exit 137 remains
its original exit status; this guard establishes image drift, not the mechanism
behind a killed process. [8](#source-8) [9](#source-9) [10](#source-10)

[FACT] Regressions cover the classification through a fake holder and the
relay, both attach paths end to end with an older protocol, every remedy and
pass-through, the bounded host-only view, a real resolver batch whose
inspection never answers (one listing, work only for the named boxes, ended by
the overall bound), and the offer's script, prompt, flag, recreation-only,
unavailable, unknown and workload cases. They use fakes; no
live update was run. [5](#source-5) [6](#source-6)

## Sources

1. <a id="source-1"></a> [View refusals](../../internal/session/view.go#L84-L180) — `Relay` first-frame check, `protocolMismatch`, `foreignHello`, `viewRefusal`; `attachView` at L194 and L288; `buildMismatch` and `holderMismatchReason` in stats.go.
2. <a id="source-2"></a> [Remedies](../../cmd/yoloctl/session_remedy.go#L1-L168) — `sessionRemedyOf`, `staleSessionInspectTimeout`, `resolveStaleSessionsFn`, `staleSessionError`, `staleSessionGuidance`, `conversationNote`, `staleSessionMessage`, `boxDriftMethod`, `collectBoxDrift`; callers in session_client.go `attachToSession` L121 and L207.
3. <a id="source-3"></a> [Restart offer](../../cmd/yoloctl/controller_sessions.go#L250-L391) — `setAsideRecreations`; the `drift` closure L75, `restartConversationNote`.
4. <a id="source-4"></a> [Controller view](../../cmd/yoloctl/controller_views.go#L256-L261) — `box.drift` in `runControllerView`; its registry entry in controller_internal.go L77.
5. <a id="source-5"></a> [Classification regressions](../../internal/session/view_mismatch_test.go#L18) — `TestViewRefusalsClassifyOnlyExplicitBuildAnswers`.
6. <a id="source-6"></a> [Remedy regressions](../../cmd/yoloctl/session_remedy_test.go#L1) — attach paths, guidance cases, `box.drift`, `TestBoxDriftBatchHasOneInventoryAndOneBound`, the offer.
7. <a id="source-7"></a> [Named resolution](../../internal/boxstate/boxstate.go#L225-L421) — `ResolveNamed` and `resolveSpace`; spy regression `TestResolveNamedLimitsPerBoxWorkToTheNamedBoxes` in boxstate_test.go L245.

8. <a id="source-8"></a> [Fresh-session guard](../../cmd/yoloctl/session_remedy.go#L174) — `guardFreshSessionImage`; caller in session_client.go before `beginTrackedSession`.
9. <a id="source-9"></a> [Shared diff](../../internal/agentbox/diff.go#L179) — `Diff`, `imageRecreateReason`, `ImageRecreateReason`.
10. <a id="source-10"></a> [Guard regressions](../../cmd/yoloctl/session_image_test.go#L1) — isolated resolver, live-holder and preserved exit status.
