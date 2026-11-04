# Following agent conversation switches

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] A record keeps two identities. `assigned` (`id`, `at`, `source`) is
the record's conversation: `chosen` when yoloctl passed it (records without a
source read as chosen), `learned` when a learn launch's first report named it,
`switched` when a resume adopted the last observed one. `current` (`id`,
`source`, `at`, `read_at`, `generation`) is the conversation the agent last
reported starting, kept across launches. It is the last observed one, never a
claim about what the agent shows now; the TUI's present selection is not
recorded. [1](#source-1) [2](#source-2)

[FACT] Only a main-session `SessionStart` of the current launch generation
that names a conversation sets `current`, with source `startup`, `resume`,
`clear`, `fork` or none; `compact` never changes it. Subagent lines and other
generations' lines are ignored, as for every fold. The same ID again refreshes
`at`, `read_at` and `generation` only. Status lines never set it.
[3](#source-3) [4](#source-4)

[FACT] The next resume requests `Target`: `current` when set, else
`assigned`. When they differ, `Allocate(resume)` first moves `assigned` to
`previous` as `switched` (with `to` and `source`) and assigns the target as
`switched`, so the ownership check, `resume.requested` and argv name one ID.
`checkOwner` checks that target and matches another box's record by its
assigned or current ID. Divergence and a `mismatch` outcome no longer refuse;
`observed.diverged` stays as evidence shown as a switch. The remaining
refusals are an uncertain launch, another owner and an invalid record.
[2](#source-2) [5](#source-5) [6](#source-6)

[FACT] `--new-session` and the `never` policy retire both identities:
`assigned`, and `current` when it differs, go to `previous` under the same
reason, and a fresh conversation starts. [1](#source-1) [5](#source-5)

[FACT] A profile with a `session:` block but no `assign_args` plans a `learn`
launch while nothing is assigned: today's argv (command, default args, extra
args) with generation, token and `YOLOCTL_AGENT_GEN`, no ID. Its first main
`SessionStart` sets `assigned` (`learned`, agent time) and `current`; without
a report nothing is assigned and the next start learns again. Claude has
`assign_args`, so it never learns; Codex has none, so its launches learn.
[2](#source-2)
[3](#source-3) [7](#source-7)

[FACT] A tombstone close records the exit status (`launch.exit_code`). After a
successful read of the generation, `SettleResume` marks a closed resume launch
`failed` (detail: the exit) when it exited nonzero, its outcome is still
`unverified` and its health is `ok` or `no-report`. Gaps, unreadable or
unobservable generations decide nothing. The next start requests the same ID
again and prints `warning: the previous resume of ID failed (DETAIL);
retrying — yoloctl box restart BOX --new-session starts a new conversation`;
it never starts fresh. Views show a read-time failure as `pending_outcome`.
[2](#source-2) [5](#source-5) [8](#source-8)

[FACT] `session.observe.selection` is `reported` or `unknown` (default).
Claude declares `reported`; Codex declares `unknown`, because an in-app pick
reports nothing until a turn. Views word the limit from it, never from the agent
name. [9](#source-9)

[FACT] `box session` prints `Conversation: ID  assigned TS (SOURCE)`, or
`none yet (learned from the agent's first report)` for a learning profile; a
`Current:` line with source, time and `next start resumes it` only when it
differs from the assigned ID; `Selection: reported by the agent` or `not
reported by AGENT: an in-app switch to an already loaded conversation is not
visible to yoloctl`; `Previous` entries as `switched to ID (SOURCE)`. The
shared JSON adds `assigned_source`, `current` and `selection`;
`schema_version` stays 1. [10](#source-10) [11](#source-11)

[FACT] The chat details card adds `Conversation  <first 8>… · LABEL` only for
the running verified launch (the `observedRuntime` gate) whose `current` is of
this launch: `switched` when it differs from the assigned ID, `learned` for a
learn launch, `resumed` for a resume reported (or view-decided) resumed,
`fresh` for an assign launch reporting `startup`. Anything else has no row; the
participant line stays `Model (effort) · away`. [12](#source-12)
[13](#source-13)

[FACT] Tests use fixtures only: no real Claude switch, missing-ID resume or VM
is exercised here. [7](#source-7) [8](#source-8) [13](#source-13)

## Sources

1. <a id="source-1"></a> [Record](../../internal/agentsession/record.go#L83-L128) — `Record.Current`, `Assigned`, `Current`; sources and reasons L37-L81; `Validate` L244; `CurrentSource` L340; `retire`, `addPrevious`, `Target` L447-L479.
2. <a id="source-2"></a> [Allocation](../../internal/agentsession/launch.go#L108-L219) — `Allocate` (learn, switch adoption), `CloseExit`, `SettleResume`.
3. <a id="source-3"></a> [Fold](../../internal/agentsession/apply.go#L86-L263) — `Apply` rules, `applyLine` (learned assignment, `current`).
4. <a id="source-4"></a> [Fold regressions](../../internal/agentsession/apply_test.go#L307) — `TestApplyCurrentFollowsTheLastMainStart`, `TestApplyLearnsTheFirstReportedConversation`.
5. <a id="source-5"></a> [Plan](../../cmd/yoloctl/session_identity.go#L162-L277) — `plan`, `uncertainError`, `checkOwner`, `namesConversation`; target check in `launch` L284; `CloseExit` in `noteExit` and `settle`.
6. <a id="source-6"></a> [Plan regressions](../../cmd/yoloctl/session_observe_test.go#L205) — `TestReportedMismatchResumesTheReportedConversation`, `TestTheNextStartResumesTheLastObservedConversation`; owner by `current` in session_identity_test.go `TestResumeRefusesAConversationOwnedElsewhere`.
7. <a id="source-7"></a> [Learn regressions](../../cmd/yoloctl/session_observe_test.go#L678) — `TestBoxSessionTextNamesALearnedConversation`; `TestArgvLearnsWithoutAnID` and `Argv` in policy.go L92-L134.
8. <a id="source-8"></a> [Failed resume](../../cmd/yoloctl/session_observe.go#L174-L195) — `observeCurrentLaunch` settles after a successful read; regressions `TestAFailedResumeIsRetriedNeverReplaced`, `TestSettleResumeDecidesOnlyAReadSilentFailure`.
9. <a id="source-9"></a> [Selection](../../internal/agentprofile/profile.go#L172-L216) — `ObserveSpec.Selection`, `SelectionReport`; validation in `validateSession`; builtin/claude.yaml and builtin/codex.yaml `session.observe`.
10. <a id="source-10"></a> [Status](../../internal/agentsession/status.go#L18-L175) — `Status` fields, `CurrentView`, `PreviousView.Text`, `LearnsID`, `Switched`; `Describe` L237.
11. <a id="source-11"></a> [box session](../../cmd/yoloctl/box_session.go#L157-L235) — `writeBoxSession`; keys in box_session_test.go `checkConversationShape`.
12. <a id="source-12"></a> [Card row](../../cmd/yoloctl/chat_participant_runtime.go#L36-L106) — `participantRuntime`, `participantConversation`, `resumeOutcome`; `AgentRuntime.Conversation` and `runtimeRows` in internal/chatui/runtime.go.
13. <a id="source-13"></a> [Card regressions](../../cmd/yoloctl/chat_participant_runtime_test.go#L75) — `TestParticipantConversationIsCertainOnly`; the card row in internal/chatui/ui_package_test.go `TestParticipantsShowObservedModelAndEffortOnly`.
