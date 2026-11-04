# Agent state and used-only context

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; stale-read note `1d265df3cfae49026ec4202fe88e9cce83ad1ca9`; re-read cited symbols when changing behavior.

[FACT] A report may carry an enumerated `state`: `working`, `idle`,
`waiting_permission`, `waiting_input`, `waiting_dialog` or `failed` (an API
error the agent reported: a Claude turn ended on one, or one Codex model
request failed). The reader accepts it on SessionStart, SessionEnd,
Stop, `statusline`, `query`, UserPromptSubmit, Notification, StopFailure,
PostModelRequest and PermissionRequest lines, never on subagent evidence.
Notification and PermissionRequest lines carry only a state: a model, effort,
source or context rejects them. As with efforts, any other value rejects the
line; a writer drops values it does not know before writing. Context may come
from `statusline`, `query` and PostModelRequest lines, and `ctx_used` alone is
a context of unknown size (`Size` 0, `Percent` -1). [1](#source-1) [2](#source-2)

[FACT] The fold keeps the launch's latest state in line order, with its
source (`hook`, `statusline` or `query`), the agent's time and the read time.
One rule resolves a wait: a `statusline` or `query` line without a state turns
`waiting_*` into `working`, since Claude's status line hides while a prompt is
open and runs on the next assistant message. A completed Codex model request
(a PostModelRequest line reporting `working`) only clears `failed`, and only
when its session is the folded conversation (current, else assigned):
otherwise it changes no state, so a completion recorded after a Stop or a
wait leaves it. The rule keys on the event, not the agent: only the Codex
writer records model requests, so a fabricated normalized line could clear
another agent's failure (a writer contract, not authenticated provenance).
Line order is not request order, and a Codex Stop still reports `idle` over
an unrecovered failure. Nothing else changes a state
without reporting one; subagent lines are skipped before any value; a new
launch starts unknown. A stored context of unknown size has no `size` or
`percent`, and `Validate` requires a known state kind and source.
[3](#source-3) [4](#source-4) [5](#source-5)

[FACT] The shared public view adds `observed.state` (`{kind, source, at,
read_at}`); `context.size` and `context.percent` are omitted when unknown, so
`box show`, `box list` and `box session` share one shape.
`box session` prints `state waiting for permission (agent-reported by hook …)`
and `context 172000 tokens used`; its summary line names the state the same
way. It has no telemetry and shows reported state only. [6](#source-6) [7](#source-7)

[FACT] The chat view maps the inventory's state into the participant runtime.
The details card's `State` row reads `working`, `idle`, `waiting for permission
since 14:02`, `waiting for input …`, `waiting for a dialog …` or `API error at
14:02`, with the report's source text. The Participants panel tags a name only
for a wait (`⏳ needs permission`, `⏳ needs input`, `⏳ dialog`, warning tone) or
`failed` (`⚠ API error`, error tone); `working` and `idle` get no words and the
card detail line is unchanged. A failed or old read shows no reported state
and does not fall back to activity: it is a read problem, and the dot still
shows activity.
Context without a size reads `40k used`. [8](#source-8) [9](#source-9) [10](#source-10)

[FACT] Only when the launch reported no state, the chat view falls back to
the box's presence activity (the shared tracker behind the pulsing dot, gated
by current telemetry and inventory): active is `working`, idle is `idle`,
unknown is no row. The card marks it as from the box's activity; it is never a
wait or a failure, so it is never tagged, and agentless boxes have no state.
[8](#source-8) [11](#source-11) [10](#source-10)

## Sources

1. <a id="source-1"></a> [Reader](../../internal/agentsession/observe/read.go#L85-L112) — `Context.Valid`, the `State*` constants, `ValidState`, `Waiting`; `stateEvents`, `contextEvents`, `stateOnlyEvents` at L437-L450, `wireEvent.context` at L413, `parseLine` at L452-L499.
2. <a id="source-2"></a> [Reader regressions](../../internal/agentsession/observe/observe_test.go#L795-L839) — `TestReadStateAndUsedOnlyContext`: state values and events, subagent and state-only lines, used-only context.
3. <a id="source-3"></a> [Fold](../../internal/agentsession/apply.go#L257-L290) — `applyLine`: context from any line carrying one, the state rule, the Codex recovery and the wait resolution.
4. <a id="source-4"></a> [Record](../../internal/agentsession/record.go#L1-L501) — `ObservedState`, `ObservedContext` with optional size and percent, the state and context checks in `Validate`.
5. <a id="source-5"></a> [Fold regressions](../../internal/agentsession/apply_test.go#L461-L711) — `TestApplyFoldsTheAgentState`: order cases, subagents, used-only context, line and details text; `TestApplyClearsACodexFailureOnlyOnACompletedRequest`: recovery and its negatives; state validation cases at L293.
6. <a id="source-6"></a> [Public view](../../internal/agentsession/status.go#L143-L170) — `StateView`, `StateText`; `ContextView.text` at L407, `Lines` at L368, `Line` at L420.
7. <a id="source-7"></a> [Shape regressions](../../cmd/yoloctl/box_session_test.go#L100-L170) — `statusFixture`, `checkConversationShape` with the state keys.
8. <a id="source-8"></a> [Chat state](../../internal/chatui/runtime.go#L26-L50) — `RuntimeState`, the state constants, `SourceActivity`; `RuntimeContext.text` at L189, `runtimeRows` at L208, `stateOf`, `stateText`, `stateTag` at L253-L318.
9. <a id="source-9"></a> [Panel tag](../../internal/chatui/roster.go#L183-L241) — `memberCard`, `memberRuns`, `stateTagRun` at L283; the adapter in cmd/yoloctl/chat_participant_runtime.go `participantRuntime`.
10. <a id="source-10"></a> [Chat regressions](../../internal/chatui/agent_state_test.go#L1-L132) — `TestReportedStateRowAndTag`, `TestStateFallsBackToActivity`; used-only text in ui_package_test.go `TestRuntimeContextText`.
11. <a id="source-11"></a> [Activity gate](../../internal/chatui/telemetry.go#L124-L139) — `boxActivity`: `participantBox`, `spacetelemetry.ActivityCurrent` and the tracker's `State`.
