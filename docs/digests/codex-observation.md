# Codex hook reporter

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; query version from initialize `f28bf6f93e5c002fd127e67c6c671c7aadc5bad2` and accompanying changes; states, tokens used and the refresh read `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; completed-request recovery `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] The `yolo.agent.observe-codex` feature installs the shared observation
writer (the same interpreter check, `agent-observe.py` and build-time Python
resolution as `yolo.agent.observe`) with its own launcher,
`/usr/local/libexec/yoloctl/agent-observe-codex`, which runs the writer in
`codex` mode. A separate launcher exists because the shared hook launcher
passes no mode. The factoring leaves the Claude recipe byte for byte
unchanged. [1](#source-1) [2](#source-2)

[FACT] It writes `/etc/codex/config.toml`, Codex's System configuration layer,
with only a hooks table: one command hook per kept event, `SessionStart`,
`Stop`, `SessionEnd`, `SubagentStart`, `UserPromptSubmit`, `PostModelRequest`
and `PermissionRequest`. Codex runs System hooks as
managed hooks without a per-hook trust prompt. The hooks are async except
`SessionEnd`, which Codex always runs synchronously; it carries
`timeout = 2`, and the writer bounds itself to a 0.5 s lock wait. The file
sets no feature flag, model or approval key, and the account's own
`~/.codex/config.toml` and `hooks.json` are never written.
[1](#source-1) [3](#source-3)

[FACT] In `codex` mode the writer keeps the hook event, `session_id` lowered
to a canonical UUID (dropped otherwise), `source` on `SessionStart` from the
reader's list (startup, resume, fork, clear, compact) and the `model` slug.
`SubagentStart` lines are marked as subagent evidence. `transcript_path`,
`cwd`, `permission_mode` and `turn_id` are discarded; unknown events write
nothing. Synthetic 0.164 vectors pin the lines until a live capture replaces
them. [4](#source-4) [5](#source-5) [6](#source-6)

[FACT] The hooks also report the state and the tokens used:
- `UserPromptSubmit` is `working` and a main `Stop` is `idle`.
- `PermissionRequest` is a state-only `waiting_permission` line, without the
  child's session when a child agent asked: a child's approval waits for the
  user like the main thread's. The next main line with a state replaces it,
  so a refresh read of the main thread can clear a wait a child still holds.
- `PostModelRequest` gives `ctx_used` from `token_usage.total_tokens`,
  `failed` when its status is failed and `working` when it is exactly
  `completed`; the fold uses that only to clear a failed request of the same
  conversation, since Codex retries a failed request itself [11](#source-11).
  Other statuses carry no state. It records neither its model nor its error.
- A child agent's prompt submit or model request writes nothing.
- Prompts, tool input and error text are never kept.

Codex context is therefore tokens used only: no window, no percentage.
[4](#source-4) [5](#source-5) [6](#source-6) [11](#source-11)

[FACT] On a main `SessionStart` or `Stop` the writer then queries the box's
own Codex app-server daemon:
- **Where:** the directory `/tmp/codex-daemon-<uid>` opened without following
  links, owned by the user with mode 0700; at most 16 user-owned sockets in
  it, tried in name order.
- **Bound:** 500 ms in total; a refused or resetting socket is skipped.
- **Read:** SO_PEERCRED must name a process of this user whose argv is
  `codex app-server`; then one WebSocket upgrade of `/rpc`, `initialize`, and
  one `thread/read` with `includeTurns: false` for exactly the hook's thread.
  Notifications and server requests are never answered.
- **Recorded only** when the peer is the same process afterwards, the thread ID
  is exact, its status is idle or active and both `model` and an enumerated
  `reasoningEffort` are present: a `query` line with the model and effort,
  labelled with the thread's `session_id`. It also carries `version` when the
  `initialize` response's `userAgent` begins with a `CLIENT/VERSION` token
  whose version is a bounded dotted number, such as `codex-tui/0.166.0` →
  `0.166.0`. The client name is not pinned; anything else writes no version.
  After a `SessionStart` it also carries the state: `idle`, or `working`,
  `waiting_permission` or `waiting_input` from the active flags. After a
  `Stop` it carries none, since the daemon may still call a finishing turn
  active.

Nothing else is written. The socket name is not pinned: a daemon that moves
its directory makes Codex boxes report the hook's model only, silently. A
launch with `--no-daemon`, `--profile` or a `-c` override other than boolean
`features` keys, `suppress_unstable_features_warning` or
`tui.fullscreen_transcript` runs no daemon, so it also reports model only.
The `thread/read` fields used are unchanged between Codex 0.164.0 and 0.165.0.
[4](#source-4) [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] A later TUI reuses a running daemon without comparing its
environment, so the holder stops `codex app-server` processes before each
managed launch and after its exit, and each launch's TUI starts a daemon of
its own. A stale socket file a killed daemon leaves is refused and skipped.
[6](#source-6) [9](#source-9)

[FACT] The controller also reads each box's daemon at refresh time, through
the writer's `codex-query` mode [10](#source-10).

## Sources

1. <a id="source-1"></a> [Codex observe feature](../../internal/features/agent_observe.go#L28) — `agentObserveCodexTOML`, `agentObserveCodexInstallDockerfile`, `agentObserveWriterInstall`.
2. <a id="source-2"></a> [Feature tests](../../internal/features/agent_observe_codex_test.go#L1) — Claude recipe golden, TOML schema and install.
3. <a id="source-3"></a> [Feature registry](../../internal/features/features.go#L93) — `yolo.agent.observe-codex`.
4. <a id="source-4"></a> [Writer](../../internal/features/agent-observe.py#L1) — `codex_record`, `CodexRead`, `daemon_sockets`, `codex_query`, `codex_hook`.
5. <a id="source-5"></a> [Codex vectors](../../internal/agentsession/observe/testdata/codex-0.164-synthetic/expected.jsonl#L1-L8) — synthetic payload lines.
6. <a id="source-6"></a> [Writer regressions](../../scripts/test_agent_observe.py#L1) — `CodexWriterTests`: fields, vectors, the daemon read and its refusals, a stale socket skipped.
7. <a id="source-7"></a> [Codex 0.165 daemon exclusion](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/tui/src/daemon_startup.rs#L61) — overrides that run no daemon.
8. <a id="source-8"></a> [Codex 0.165 `--no-daemon`](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/tui/src/cli.rs#L87) — the explicit switch.
9. <a id="source-9"></a> [Agent daemon lifetime](agent-daemons.md) — the holder's reaper and its limits.
10. <a id="source-10"></a> [Codex refresh read](codex-refresh-read.md) — the controller's scheduler, targets and writer mode.
11. <a id="source-11"></a> [State fold](../../internal/agentsession/apply.go#L274-L288) — a completed model request clears only a matching failure; any other line with a state replaces the observed one.
