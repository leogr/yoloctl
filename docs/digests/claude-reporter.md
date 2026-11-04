# Claude status line and hook reporter

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] The Claude profile requires `yolo.agent.observe` and names it as
`session.observe.push`. A push feature must also appear in `requires_features`.
The removed `session.observe_feature` key fails validation with a rename
message. Every Claude box composes the feature whatever its declared features.
An existing box gets it with its next recreate, because the composed image
changes. No create-time prefill or opt-in remains. [1](#source-1) [2](#source-2)

[FACT] The feature's managed drop-in, `40-yoloctl-agent.json`, sets two things:

- a `statusLine` command, `agent-statusline`, without `refreshInterval` or
  `padding`;
- SessionStart, SessionEnd, PostModelSwitch, Stop, UserPromptSubmit,
  Notification and StopFailure command hooks, `agent-observe`, all `async`
  and without a timeout.

Both wrappers exec the build-resolved interpreter with `-I -S` on the same
writer, under the observation substrate's writer bounds. [3](#source-3)

[FACT] Fields each line keeps; the writer drops anything else:

- Hooks: event, session ID, start source, model, effort level, subagent flag,
  state, parent PID, UTC time and `dur_ms`. Notification keeps only state
  and its envelope (event, session ID, PID, time and duration).
- Status lines: event `statusline`, `wrapped`, an optional `handover` reason,
  session ID, `model.id`, `effort.level`, `version` and `dur_ms`. Context is
  `context_window_size` with an integer `used_percentage` (kept only as a pair).
  `ctx_used` is the sum of `current_usage` input, cache-creation and cache-read
  tokens, kept only when all three are integers (the documented basis of the
  percentage).
- Values the reader would refuse are left out, not written as invalid: an
  effort outside the reader's enumeration, or a non-printable model or
  version.
[4](#source-4) [5](#source-5)

[FACT] Main hooks map UserPromptSubmit to `working`, StopFailure to `failed`,
and Stop to `idle`, or `working` if either `background_tasks` or `session_crons`
is a non-empty array. Notification maps `permission_prompt` to
`waiting_permission`, `agent_needs_input` to `waiting_input`, both
`elicitation_dialog` and `elicitation_url_dialog` to `waiting_dialog`, and
`idle_prompt` to `idle`. Other notification types write nothing. Subagent-tagged
hooks carry no state; their notifications are omitted. Prompts, messages, tool
input, error text, task/cron contents and paths are never recorded.
[4](#source-4) [9](#source-9)

[FACT] Claude Code 2.1.312 D6-derived fixtures cover UserPromptSubmit, Stop
with/without background tasks and idle_prompt. A+ captures add Bash and
AskUserQuestion Notifications: both are `permission_prompt`, mapped to
`waiting_permission`. Cron, other wait types and failure remain synthetic.
A+ used an exec'd Claude with prompts on and session-only capture hooks;
Notifications arrived about six seconds after unanswered prompts. Separately,
ordinary managed AskUserQuestion showed/cleared `waiting_permission`. Neither
proves elicitation/input delivery or managed tool-permission prompting. These
are version/run observations, not timing guarantees; the reporter inherits
Claude's notification timing. [9](#source-9) [10](#source-10) [11](#source-11)

[FACT] As the status line, the writer:

1. reads stdin once (at most 1 MiB);
2. looks up `statusLine.command` of type `command` in
   `$CLAUDE_CONFIG_DIR/settings.json`, or `~/.claude/settings.json` when that
   variable is not absolute. The file must be a regular file of at most
   1 MiB, opened non-blocking. Anything else means no user command;
3. stages the payload as fd 0: an unnamed `O_TMPFILE` in `/tmp` first, then a
   non-blocking pipe if the payload is under 64 KiB. Otherwise the hand-over
   is skipped with `handover` `too-large` or `unavailable`;
4. records the line, then `exec /bin/sh -c` on the user command with
   `YOLOCTL_STATUSLINE_WRAPPED=1` set. The exit status and output are that
   command's.

Without a user command it prints nothing and exits 0. A missing generation or
a failed record still hands over. With the marker already set it does
nothing, so a user command naming the wrapper cannot recurse. Project and
local status lines are not run: Claude runs those only in trusted workspaces,
and the wrapper cannot apply that trust decision. [4](#source-4) [6](#source-6)

[FACT] devOpUs's S0 capture of Claude Code 2.1.311, in one disposable box:

- SessionStart carries `model` only on an interactive startup. Resume, clear
  and `-p` startups have none.
- Stop and status line payloads carry `effort.level`.
- `-p` sessions run no status line and recorded no async Stop.
- `memfd_create` and `/dev/shm` `O_TMPFILE` are refused in the confined box;
  `/tmp` `O_TMPFILE` works.
- PostModelSwitch needs `/model` and was not captured; its `to_model` is taken
  from the official hooks reference.

The captured payloads are repository vectors: the Python writer test turns
them into `expected.jsonl`, and the Go reader test parses that file. They are
facts of that version, not a contract. [6](#source-6) [7](#source-7) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Profile](../../internal/agentprofile/profile.go#L161-L230) — `ObserveSpec`, `PushFeature`, `LegacyObserveFeature`; validation at L549-L558; `requires_features` and `session.observe` in builtin/claude.yaml L45-L70.
2. <a id="source-2"></a> [Requirement regressions](../../cmd/yoloctl/session_observe_test.go#L244-L273) — `TestEveryClaudeBoxRequiresItsReporter`; profile cases in internal/agentprofile/profile_test.go (`session-observe-*`); the older container's recreate note in `TestLaunchCarriesTheGenerationOnlyWhenObservable`.
3. <a id="source-3"></a> [Feature](../../internal/features/agent_observe.go#L12-L174) — `agentObserveEvents`, `agentObserveSettingsJSON`, `agentObservePython`, `agentObserveInstallDockerfile`; async-only regression in agent_observe_test.go `TestAgentObserveSettingsRegisterOnlyTheContractHooks`.
4. <a id="source-4"></a> [Writer fields](../../internal/features/agent-observe.py#L75-L214) — `NOTIFICATION_STATES`, `EFFORTS`, `record`, `statusline_record`; `write` at L593, `user_command` L618, `stage` L683, `statusline` L779, `main` L814.
5. <a id="source-5"></a> [Field regressions](../../scripts/test_agent_observe.py#L174-L292) — vectors, status line allowlist/context and real `/tmp` staging.
6. <a id="source-6"></a> [Hand-over regressions](../../scripts/test_agent_observe.py#L294-L398) — settings lookup, record-then-exec, same bytes/status, no command, record failure, skipped hand-over and recursion.
7. <a id="source-7"></a> [S0 vectors](../../internal/agentsession/observe/testdata/claude-2.1.311/expected.jsonl#L1-L6) — payloads beside it, copied from the S0 capture (`.yolo-team/tasks/feat-runtime-metadata/S0/runE-final/payloads`, devOpUs, 2026-10-27).
8. <a id="source-8"></a> [Reader vector tests](../../internal/agentsession/observe/observe_test.go#L669-L724) — `TestReadAcceptsTheClaudeVectors`, `TestReadAcceptsTheClaudeStateVectors`; context-only case `TestReadKeepsAContextWithoutUsedTokens` at L780.
9. <a id="source-9"></a> [State fixtures](../../internal/agentsession/observe/testdata/claude-2.1.312-state/payloads.json#L1-L127) — per-case capture provenance and synthetic boundaries; `test_claude_state_vectors`, privacy, unknown-notification, subagent and task-array regressions in scripts/test_agent_observe.py L186-L243.
10. <a id="source-10"></a> [Official hooks reference](https://code.claude.com/docs/en/hooks#notification) — Notification timing/types, `stop-input` arrays and `stopfailure-input`, checked 2026-10-28.

11. <a id="source-11"></a> [Capture limits](../../internal/agentsession/observe/testdata/claude-2.1.312-state/SOURCE.txt#L1-L16) — raw hashes, normalization, exec scope and remaining synthetic cases.
