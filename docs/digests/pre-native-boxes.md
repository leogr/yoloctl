# Pre-native boxes

Verification: `d7637173ad541fc85a6a225a59a5dc4e71f42644` and accompanying changes; re-read cited sources when changing behavior.

[FACT] A pre-native box is an existing container whose inspected evidence has
no native OpenShell runtime signal and is not a privileged launch. Mixed or
incomplete native evidence stays native, a privileged box keeps its own
endpoint route and a missing container is ordinary creation. The
classification reads only inspected evidence; it does not need the retired
OpenShell binary, whose hash check was removed. [1](#source-1) [2](#source-2)

[FACT] A pre-native box is never started or restarted. The shared native
preparation and policy-restart qualification return the typed
`LegacyRuntimeError` before any effect; `LiveApplier.Restart` has no legacy
branch, and the existing-start path refuses a non-native, non-privileged
container before its socket, cache or engine start. A running one keeps its
current run: reads, attach to a live session and a start of a running box are
unchanged. [1](#source-1) [2](#source-2) [3](#source-3) [6](#source-6)

[FACT] At the CLI boundary only that typed error becomes a remedy. Restart,
start and revival classify an existing container's actual runtime before the
generic recreate-drift refusal, so a pre-native box with recreation drift
still takes this remedy; a failed read or native evidence leaves the drift
checks unchanged. A restart
(including the `--restart-sessions` legs) and a policy apply that restarts
first stop the exact run through the shared session-aware stop; a policy apply
also ends and records its session and adds "The saved policy was not
reverted." A start, revival or chat server start never stops. After a stop that
a read confirms, the text is "Box NAME predates the native runtime and cannot be
started or restarted. It is stopped. Recreate it to continue: yoloctl box
recreate NAME --space SPACE."; a failed or unconfirmed stop says so instead
and never "It is stopped". Other errors are returned unchanged. [4](#source-4) [6](#source-6)

[FACT] Only that confirmed-stopped refusal puts the reserved `remedy`
attribute `legacy-recreate` on the worker's failed outcome record. The client
copies it into `ExitError.Remedy` from the last explicit outcome record, only
for a known failure (no `uncertain`, `command-uncertain` or `interrupted`
state). [8](#source-8)

[FACT] `box.inspect` reports `legacy_runtime` from the launcher read. The host
skips the pre-dispatch recreate offer for such a box. After a start, restart
or attach (or `box set`/`box reconcile` of a box read running before it) whose
result carries that typed remedy, a person at the terminal is asked once, only
when a fresh read still shows it pre-native and stopped: "Recreation deletes
files stored only in the container. Workdir, declared cache and account state
are preserved. Recreate NAME now? [y/N]". A lost reply, an uncertain result,
an unrelated error or an unconfirmed stop asks nothing and keeps its result,
whatever a later read shows. Default No, EOF and non-interactive use keep the
refusal. A yes sends one ordinary `box recreate NAME --force --no-attach
--legacy-remedy`; neither it nor the original request is retried or sent
again. [5](#source-5) [6](#source-6)

[FACT] The hidden `--legacy-remedy` marker only adds cleanup: a failure after
that recreate's replacement started (session start or autoupdate) is settled
by box start's result policy, bound to the run the operation started
(`ExpectedRuns.Started`, kept across its own stops, which clear the
expectation). A running read must be that run (`SameRun`: ID, start time and
PID when known); a stopped read of that container is the replacement after its
stop. Another container, another run of it or an unknown/incomplete identity
is refused before any cleanup effect and the failure says so. No previous
runtime or policy is launched; other recreates are unchanged. [9](#source-9) [6](#source-6)

[FACT] `--restart-sessions` never sets a pre-native box aside as "left
running": its restart is attempted, the other boxes continue, a chat or
workload leg names the recreate command before its recovery step, nothing is
recreated, and the run fails naming each recreate command. Unrelated
recreation drift is still set aside. [7](#source-7) [6](#source-6)

[FACT] Tests use fake engines and controller targets; they are not live
validation on a retained pre-native VM. [6](#source-6)

## Sources

1. <a id="source-1"></a> [internal/agentbox/openshell_legacy.go](../../internal/agentbox/openshell_legacy.go#L14) — `LegacyRuntimeError`, `LegacyRuntime`; route use in `openshell_policy_preflight.go` `qualifyNativeRuntimeRoute`.
2. <a id="source-2"></a> [internal/agentbox/openshell_legacy_test.go](../../internal/agentbox/openshell_legacy_test.go#L14) — classification cases; `openshell_start_test.go` refusal without retired-binary read.
3. <a id="source-3"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L424) — `LiveApplier.Restart`; `cmd/yoloctl/agentbox.go` `startExistingAgentboxContainerPrepared`, `qualifyNativeRuntimePreparation`.
4. <a id="source-4"></a> [cmd/yoloctl/legacy_runtime.go](../../cmd/yoloctl/legacy_runtime.go#L46) — `refuseLegacyRun`, `legacyRuntimeFirst`; callers in `agentbox.go` (start, `preflightAgentboxRestart`), `session_client.go`, `chat_runtime.go`, `managed_applier.go` `Restart`.
5. <a id="source-5"></a> [cmd/yoloctl/recreate_offer.go](../../cmd/yoloctl/recreate_offer.go#L107) — `boxLegacyRecreateOffer`, `offerLegacyRecreate`; wiring in `controller_client.go` `callControllerTerminal`; `inspectIdentity.LegacyRuntime`; `boxstate.Box.LegacyRuntime`; `launcher_linux.go` `BoxLauncher`.
6. <a id="source-6"></a> [cmd/yoloctl/legacy_runtime_test.go](../../cmd/yoloctl/legacy_runtime_test.go#L96) — stop/refusal, unconfirmed stop, policy apply, offer, terminal path, restart-sessions and revival; `chat_runtime_output_test.go` chat server refusal.
7. <a id="source-7"></a> [cmd/yoloctl/controller_sessions.go](../../cmd/yoloctl/controller_sessions.go#L339) — refused legacy restart leg; `setAsideRecreations`; `session_remedy.go` `boxDriftView.LegacyRuntime`.
8. <a id="source-8"></a> [internal/controller/client.go](../../internal/controller/client.go#L31) — `ExitError.Remedy` set in `Call` from the last outcome; `pkg/prettylog` `KeyRemedy`, `Structure.Remedy`; `cmd/yoloctl/legacy_runtime.go` `outcomeRemedy` used by `cli_logging.go` outcomes; `client_log_test.go` `TestCallExitCarriesOnlyTheLastOutcomeRemedyOfAKnownFailure`.
9. <a id="source-9"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L2616) — `recreateAgentbox` remedy path through `runStartForResult`; `legacy_runtime.go` `exactRunStopSteps`, `legacyRemedyFlag`, `legacyRuntimeFirst`; `internal/container/expected_runs.go` `ExpectedRuns.Started`, `SameRun`, `recordStarted`; `box_stop_result.go` `boxTarget.Run`.
