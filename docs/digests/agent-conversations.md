# Agent conversation identity

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; resume carry `ffd5153aed3db8e29d362837407e24bdfacb5f7e` and accompanying changes; resume-file preflight on `f5928f39223f8851c230b898d6eb2fcfab16cc2c` and accompanying changes; re-read cited sources when changing behavior.

Launch locking, tokens and settlement are detailed in
[tracked agent launches](agent-launches.md).

[FACT] A profile's optional `session:` block declares conversation identity:
a `uuid` format, `assign_args` (fresh ID; without them the agent's first
report names it), `resume_argv`, optional native `resume_file` presence check,
identity-taking `reserved_args` and `observe.selection`. Claude declares it
(`--session-id`, `--resume`) and
Codex too: no assign arguments, so a launch learns the thread from the first
main `SessionStart` it reports, `codex resume <id>` ahead of the ordinary
options, `resume` reserved and selection unknown. agy's block is
observe-only (a reporter, no `resume_argv` or `assign_args`): unset means
`never`, `last` is refused, and every start learns a new conversation
([agy reporter](agy-observation.md)). Other built-ins stay unsupported until
proven. [1](#source-1) [2](#source-2) [3](#source-3) [19](#source-19)

[FACT] Claude's opt-in `resume_carry` appends the resumed conversation's
reported effort/model after box arguments (Claude takes the last). Only values
attributed to that conversation apply: effort `low|medium|high|xhigh|max`,
plain model ID (no whitespace or leading `-`). Capture precedes the new
observation. Fresh assignments and foreign/unattributed reports carry nothing.
Eligible values override later box model/effort edits; `--new-session` on
start/restart/attach seeds a fresh conversation with box arguments instead.
Without eligible values, box arguments apply. Codex declares no carry and
receives its ordinary resume argv plus box arguments; thread behavior remains
Codex's own. agy always starts new. [1](#source-1) [3](#source-3)
[20](#source-20)

[FACT] A manifest's `agent_session.resume` is `last`, `never` or `manual`.
Unset means `manual` when `agent_extra_args` carry a reserved argument, else
`last`. An explicit
`last`/`never` with one is refused at create, set, edit and start. Manual, unsupported and agentless boxes are
untouched. [3](#source-3) [4](#source-4)

[FACT] The controller-owned record lives at
`<space config>/state/boxes/<box>.session.yaml`, outside `boxes/`. It is
strictly decoded, replaced atomically and scoped by account, agent and box ID;
another scope's record is retired (`account-changed`). `last_generation`
survives retirement, so generations are never reused. Destroy removes the
record first, and a failed removal keeps the declaration and fails, so a
same-name box never inherits it. Export omits records: an import starts fresh
tracking and assigns on first attach, while same-disk space recreate keeps
them. [5](#source-5) [6](#source-6) [7](#source-7) [17](#source-17)

[FACT] Under `last` the next launch resumes the last conversation the agent
reported, else the assigned one (see [following switches](conversation-following.md)),
unless another box of the same account and agent runs, dispatched or left
uncertain it under either ID; an unreadable record or probe refuses. A
reported `mismatch` or divergence refuses nothing. `never` and `--new-session`
move both IDs to `previous` (at most 8) and assign a fresh one.
`--new-session` on start or attach refuses a live holder first; restart
retires after its teardown, and `--no-attach` launches nothing. Exit status
alone is never a resume verdict; a nonzero resume exit with no reported start
is `failed`. Failure itself never selects a fresh conversation. [8](#source-8) [9](#source-9) [10](#source-10)

[FACT] Before allocating a new resume, Claude checks the Target's saved file
in its native leaf. Confirmed absence retires both IDs as
`saved-conversation-missing` and selects one fresh UUID; inaccessible or
incomplete storage refuses. An allocated launch is reused without a new check.
The notice and shared status explain the replacement. Presence does not prove
resumability; see [saved-file checks](resume-file-presence.md).
[8](#source-8) [21](#source-21) [22](#source-22)

[FACT] Views read the record without locks. `box show` prints a
`Conversation:` line. `box show --json`, `box list --json` and the read-only
`box session NAME [--json]` share one snake_case `conversation` object that
never contains the launch token or observation cursor. `box session` (in
`box.show`'s registry class) adds the holder state: running, not-running, lost
or unknown, an undecidable probe never shown as idle. It reads no transcripts
and changes nothing. The restart offer names each box's next conversation,
never a resume. [11](#source-11) [12](#source-12) [13](#source-13)
[18](#source-18)

[FACT] Record, launch, ownership, policy, argv and status regressions use
fixtures; no real Claude, VM, crash or reboot is exercised.
[14](#source-14) [15](#source-15) [16](#source-16)

## Sources

1. <a id="source-1"></a> [profile.go](../../internal/agentprofile/profile.go#L150) — `Profile.Session`, `SessionSpec`, `ObserveSpec`; `validateSession`.
2. <a id="source-2"></a> [claude.yaml](../../internal/agentprofile/builtin/claude.yaml#L56) — the Claude block.
3. <a id="source-3"></a> [policy.go](../../internal/agentsession/policy.go#L12-L147) — `EffectivePolicy`, `ReservedArg`, `Argv`.
4. <a id="source-4"></a> [agentbox.go](../../internal/agentbox/agentbox.go#L99-L178) — `AgentSession`, `ResumePolicy`; validation at L524-L533; cmd checks in `requireResolvableManifest`.
5. <a id="source-5"></a> [layout.go](../../internal/layout/layout.go#L355) — `BoxSessionPath`.
6. <a id="source-6"></a> [record.go](../../internal/agentsession/record.go#L86) — `Record`, `Validate`, `Load`, `Remove`, `save`, `retire`, `Target`.
7. <a id="source-7"></a> [launch.go](../../internal/agentsession/launch.go#L15-L105) — `BoxLock`, `LoadLocked`, `Save`; destroy in `removeAgentboxConfig`.
8. <a id="source-8"></a> [session_identity.go](../../cmd/yoloctl/session_identity.go#L170) — `plan`, `launch`, `uncertainError`, `checkOwner`, `namesConversation`.
9. <a id="source-9"></a> [session_identity.go](../../cmd/yoloctl/session_identity.go#L390) — `retireForNewSession`, `bindLiveLaunch`, `liveHolderError`.
10. <a id="source-10"></a> [session_identity.go](../../cmd/yoloctl/session_identity.go#L121-L160) — `noteExit`, `exitHint`, `noteHolderLost`.
11. <a id="source-11"></a> [status.go](../../internal/agentsession/status.go#L19) — `Status` and its view types, `Describe`, `Line`.
12. <a id="source-12"></a> [boxstate.go](../../internal/boxstate/boxstate.go#L144-L200) — `Box.Conversation`, resolver hook; filled at L366-L368.
13. <a id="source-13"></a> [controller_sessions.go](../../cmd/yoloctl/controller_sessions.go#L395) — `restartConversationNote`, which names the last reported conversation after a switch.
14. <a id="source-14"></a> [agentsession_test.go](../../internal/agentsession/agentsession_test.go#L1) — record, transitions, policy, status.
15. <a id="source-15"></a> [session_identity_test.go](../../cmd/yoloctl/session_identity_test.go#L1) — tracked lifecycle with fakes.
16. <a id="source-16"></a> [launch_token_test.go](../../internal/session/launch_token_test.go#L1) — legacy decoding, token format; holder round trip in `TestHolderLifecycle`.
17. <a id="source-17"></a> [session_records_test.go](../../internal/spacearchive/session_records_test.go#L1) — export omission; readDeclarations in state.go L30-L74.
18. <a id="source-18"></a> [box_session.go](../../cmd/yoloctl/box_session.go#L1) — view, probes and output; tests in box_session_test.go.
19. <a id="source-19"></a> [codex.yaml](../../internal/agentprofile/builtin/codex.yaml#L83-L97) — the Codex block; `TestCodexLearnsAndResumesByThread` in internal/agentsession/agentsession_test.go.
20. <a id="source-20"></a> [Resume carry](../../internal/agentsession/policy.go#L1-L208) — `Carried`, `CarryArgs`, `plainModelID`; `ObservedValue.Session` set in apply.go `applyLine`; `trackedLaunch.launch` in cmd/yoloctl/session_identity.go; tests carry_test.go and `TestResumeCarriesTheConversationsLastEffortAndModel`.
21. <a id="source-21"></a> [session_resume_file.go](../../cmd/yoloctl/session_resume_file.go#L15) — bounded native presence request and explicit outcomes.
22. <a id="source-22"></a> [session_resume_file_test.go](../../cmd/yoloctl/session_resume_file_test.go#L21) — absent/saved/unknown, Target, replay and persistent status regressions.
