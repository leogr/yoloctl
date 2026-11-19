# Model and effort on resume

Verification: `ffd5153aed3db8e29d362837407e24bdfacb5f7e` and accompanying changes; agy label carry on `37fa8447df51ccbea03fea6ce802418e42e3f30e` and Codex arguments on `c6e67053e4fd40a8987dc6be1c3f34bb4d6b1380`, each with accompanying changes.

[FACT] Claude's opt-in `resume_carry` appends the resumed conversation's
reported effort/model after box arguments (Claude takes the last). Only values
attributed to that conversation apply: effort `low|medium|high|xhigh|max`,
plain model ID (no whitespace or leading `-`). agy carries only the model,
in the opt-in `label` format, which also allows inner single spaces and
parentheses; it stays one argument. That agy honors the last `--model` is
unverified. Capture precedes the new
observation. Fresh assignments and foreign/unattributed reports carry nothing.
Eligible values override later box model/effort edits; `--new-session` on
start/restart/attach seeds a fresh conversation with box arguments instead.
Without eligible values, box arguments apply for profiles that declare
`resume_carry`. [1](#source-1) [2](#source-2)

[FACT] A built-in Codex tracked resume can omit the box's `-m`/`--model`
so Codex can restore the saved settings even without a fresh observation.
If a model option follows another box argument and a bare word follows its
value, the launcher keeps the original arguments to preserve the value boundary.
Put the model option first or use `--` before the prompt to allow omission.
First starts, `--new-session` and manual launches keep the model option.
Explicit Codex model, provider or effort configuration can override saved settings.
[3](#source-3) [4](#source-4) [5](#source-5)

## Sources

1. <a id="source-1"></a> [profile.go](../../internal/agentprofile/profile.go#L148-L200) — `SessionSpec`, `ResumeCarry`; `validateSession`.
2. <a id="source-2"></a> [Resume carry](../../internal/agentsession/policy.go#L196) — `Carried`, `CarryArgs`, `plainModelID`, `modelLabel`; `ObservedValue.Session` set in apply.go `applyLine`; `trackedLaunch.launch` in cmd/yoloctl/session_identity.go; tests carry_test.go, statusline_identity_test.go and `TestResumeCarriesTheConversationsLastEffortAndModel`.
3. <a id="source-3"></a> [policy.go](../../internal/agentsession/policy.go#L112-L181) — `Argv`, `withoutValueArgs`.
4. <a id="source-4"></a> [codex.yaml](../../internal/agentprofile/builtin/codex.yaml#L83-L97) — the Codex block, `resume_omit_value_args`.
5. <a id="source-5"></a> [Codex 0.170 resume configuration](https://github.com/openai/codex/blob/rust-v0.170.0/codex-rs/tui/src/app/config_persistence.rs#L31-L55) — native restore and explicit overrides.
