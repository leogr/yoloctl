# Antigravity CLI status line reporter

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; live observation and disable outcomes `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; Antigravity CLI status line, settings and hooks documentation read 2026-10-28; shared-code anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9`; test citations re-anchored on `9e0761f5f103449b59a79176c30bc3bcfbab12b0` and accompanying changes; re-read cited sources when changing behavior.

[FACT] agy documents no system settings layer: user settings live in
`~/.gemini/antigravity-cli/settings.json`, and its hooks are synchronous and
carry no state or context. The status line, configured as `statusLine` in that
user file, is the only documented source of model, effort, context and agent
state. agy runs it through `sh -c` with the JSON on stdin whenever the agent
state changes, at most every 300 ms, and stops a run after 5 s.
[1](#source-1) [2](#source-2)

[FACT] The `yolo.agent.observe-agy` feature, required by the `agy` profile,
installs the shared writer and the launcher
`/usr/local/libexec/yoloctl/agent-statusline-agy`, which runs it in `agy`
mode. It writes no image or account settings. [3](#source-3) [4](#source-4)

[FACT] In `agy` mode the writer appends one `statusline` line per run:
- `conversation_id` as `session_id` when it is a canonical UUID (lowered);
- `model.id` and `model.effort` when enumerated (`model` may be null);
- `version`;
- `context_window`: `context_window_size` with `used_percentage` (a float,
  rounded) as size and percent, `current_usage` input, cache-creation and
  cache-read tokens as used; a size of 0 is unknown and keeps the used tokens
  alone;
- `state`: `tool_confirmation_pending: true` is `waiting_permission`; else
  `agent_state` `working`, `tool_use` and `reviewing` are `working`, `idle`
  is `idle`, `error` is `failed`. `initializing`, `authenticating` and any
  other value write no state.

Queued input (`pending_input_count`), email, quota, cost, paths, titles and
everything else are dropped. Docs-backed synthetic vectors pin the lines; the
reader accepts them. [5](#source-5) [6](#source-6) [7](#source-7)

[FACT] Recording comes first; the writer then runs the status line command
that `account observe` replaced, if one was saved in
`~/.gemini/antigravity-cli/yoloctl-statusline.json` (`{"v": 1, "previous":
…}`), with the same stdin and the Claude hand-over rules. It runs only a
`previous` of type `command` (the default) that is not disabled. Without one
it prints nothing. [5](#source-5) [6](#source-6)

[FACT] Route A: the agy launcher seeds `statusLine: {type: command, command:
<launcher>, stack_with_default: true}` only when it creates a new account's
settings file and the launcher is installed; existing settings are never
rewritten. [8](#source-8) [9](#source-9)

[FACT] Route B: `yoloctl account observe NAME AGENT --enable|--disable`, for a
profile declaring `session.observe.account_status_line` (agy:
`antigravity-cli/settings.json` and the launcher). Like native import it takes
the account lock, requires the account and a running controller, and refuses
while a running box uses the account. A privileged guest action edits the file
inside the VM; its contents never leave it:
- reads without following links, at most 1 MiB, a strict JSON object (a
  duplicate key, `NaN` or another type refuses);
- `--enable` saves the current `statusLine` verbatim, then sets the reporter,
  keeping every other key and the previous line's `padding` and
  `stack_with_default`, or `stack_with_default: true` when nothing would run;
- `--enable` refuses an account with no settings file and creates nothing:
  the launcher seeds a new account, with its defaults, at the first start;
- `--disable` restores the saved line, or removes the reporter's, then removes
  the saved file; a `statusLine` changed since enabling is left alone;
- writes are atomic with file and directory fsync, mode 0600.
[10](#source-10) [11](#source-11) [12](#source-12) [13](#source-13)

[FACT] The profile's session block is observe-only: no `resume_argv` or
`assign_args`. Such a profile defaults to `never`, refuses `last`, and allows
`manual`, so every start is a new conversation tracked only for its reports.
[14](#source-14) [15](#source-15)

[FACT] Observed live 2026-10-28, agy 1.4.9, a pre-release candidate (nested
Firecracker space): agy rewrites its settings about 1 s after launch, dropping
`enableTelemetry` and `enableTerminalSandbox`, and keeps the seeded
`statusLine` through that rewrite, a stop/start and a second session.

[FACT] Not yet observed (D-d, leogr's trial): reported values from a real
login, whether an empty stacked line costs a row, and whether `current_usage`
is the basis of `used_percentage`, as it is for Claude. [1](#source-1) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Official status line docs](https://www.antigravity.google/docs/cli/statusline) — payload fields, `statusLine` keys, triggers, rate limit and timeout, read 2026-10-28.
2. <a id="source-2"></a> [Official settings docs](https://www.antigravity.google/docs/cli/settings) — user settings path, hooks files; no system layer, read 2026-10-28.
3. <a id="source-3"></a> [Feature](../../internal/features/agent_observe.go#L46-L51) — `AgentObserveAgyStatusLine`, `agentObserveAgyInstallDockerfile`; catalog entry in features.go L98.
4. <a id="source-4"></a> [Feature regression](../../internal/features/agent_observe_test.go#L139-L223) — `TestAgentObserveAgyInstall`; `STATES` against the reader in `TestAgentObserveWriterMatchesReaderBounds`.
5. <a id="source-5"></a> [Writer](../../internal/features/agent-observe.py#L211-L270) — `AGY_STATES`, `agy_percent`, `agy_record`; `read_settings`, `chained_command`, `agy_command` at L637/L655/L665, `statusline` at L779, `main` at L814.
6. <a id="source-6"></a> [Writer regressions](../../scripts/test_agent_observe.py#L419-L557) — `AgyWriterTests`, `AgyHandOverTests`.
7. <a id="source-7"></a> [Vectors](../../internal/agentsession/observe/testdata/agy-docs-synthetic/SOURCE.txt#L1) — payloads, order and expected lines; read in observe_test.go `TestReadAcceptsTheAgyVectors`.
8. <a id="source-8"></a> [Launcher seed](../../internal/features/agy-launch.py#L8-L30) — `OBSERVE`, the seeded `statusLine`.
9. <a id="source-9"></a> [Seed regression](../../internal/features/agy_test.go#L8-L47) — new and existing accounts; the installed check in scripts/smoke/agy.sh L60.
10. <a id="source-10"></a> [Command](../../cmd/yoloctl/account_observe.go#L1-L115) — `newAccountObserveCmd`; `account.observe` in controller_internal.go L90.
11. <a id="source-11"></a> [Guest action](../../internal/accountstate/guest.py#L48-L126) — `strict_object`, `load_json`, `runs_command`, `status_line`; dispatch in `run` at L182-L184 and L218; `StatusLine`, `Result.Outcome` in state.go.
12. <a id="source-12"></a> [Guest regressions](../../internal/accountstate/testdata/test_guest.py#L115-L191) — set and restore, kept keys, refusals, links.
13. <a id="source-13"></a> [Command regressions](../../cmd/yoloctl/account_observe_test.go#L1-L121) — request, outcomes, refusals, registry class, launcher match.
14. <a id="source-14"></a> [Profile](../../internal/agentprofile/profile.go#L182-L207) — `AccountStatusLine`, `Resumes`; validation at L552-L565; builtin/agy.yaml `session`.
15. <a id="source-15"></a> [Policy](../../internal/agentsession/policy.go#L52-L71) — the observe-only branch of `EffectivePolicy`; cases in agentsession_test.go `TestEffectivePolicyKeepsExistingLaunches`.
