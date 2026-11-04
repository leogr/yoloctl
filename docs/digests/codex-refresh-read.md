# Codex refresh read

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] The controller also reads each Codex box's daemon while someone looks.
- **Trigger:** a finished `box.inventory` or `box.list` read wakes a
  controller-daemon scheduler, which runs at most once per minute however many
  viewers read; reads never wait for it.
- **Admission:** each run goes through the storage-maintenance admission, so
  it skips a draining or uncertain controller and yields to every admitted
  mutation.
- **Targets:** a short-lived `controller-internal observe-codex` worker takes
  every box whose profile reports through `yolo.agent.observe-codex`, whose holder runs with
  the tracked launch's token and whose launch is `launched`. Its thread is the
  conversation `box session` shows after folding this launch's live reports:
  the current one, else the assigned one, so a learn launch's first report and
  a switch are followed. Each box's probe and fold share one 3 s budget: the
  fold's read gets a `box session` command's live-read budget, ended by the
  box's deadline when that comes first and by the worker's cancellation. [1](#source-1) [4](#source-4)
  [5](#source-5)
- **Read:** one box at a time within 3 s, it runs
  `agent-observe-codex-query GEN THREAD` as the sandbox user through the
  confined SSH endpoint `box exec` uses, with no receipt and no box lock.
- **Writer:** the generation and thread come as validated argv (a decimal
  generation, a lowercase UUID); anything else exits 2. The read keeps the peer
  binding and the CURRENT-only rule and appends a `query` line with the state,
  labelled with the thread, to the existing generation directory; with no
  daemon it writes nothing. The fold applies it only while that thread is
  still the box's conversation, so a switch during a run drops it.
[1](#source-1) [2](#source-2) [3](#source-3)

## Sources

1. <a id="source-1"></a> [Codex daemon read](../../cmd/yoloctl/controller_codex_observe.go#L1) — `scheduleCodexObserve`, `collectCodexObservation`, `codexObserveTargets`, `readCodexTargets`, `codexObserveExec`; `Server.ReadFinished` in internal/controller/server.go.
2. <a id="source-2"></a> [Daemon read regressions](../../cmd/yoloctl/controller_codex_observe_test.go#L1) — coalesced inventory storm, uncertain controller, non-blocking read, live folded targets (first report, switch, exited holder), per-box fold budget and read budget.
3. <a id="source-3"></a> [Writer refresh mode](../../internal/features/agent-observe.py#L1) — `codex_query_mode`, `CodexRead` (peer binding, current-only); launcher `AgentObserveCodexQuery` in internal/features/agent_observe.go.
4. <a id="source-4"></a> [Holder probe](../../cmd/yoloctl/box_session.go#L138-L155) — `probeBoxSession`; `resetObservationViewBudget` in cmd/yoloctl/session_observe.go.
5. <a id="source-5"></a> [Conversation view](../../internal/agentsession/status.go#L277) — `Describe`: the read-time fold and `LiveLaunch`.
