# Pre-native boxes

Verification: `4556af41c97201f4eecbb9534211460d3fbde0c4` and accompanying changes; re-read cited sources when changing behavior.

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

[FACT] At the CLI boundary only that typed error becomes a remedy. A restart
(including the `--restart-sessions` legs) and a policy apply that restarts
first stop the exact run through the shared session-aware stop; a policy apply
also ends and records its session and adds "The saved policy was not
reverted." A start, revival or chat server start never stops. After a stop that
a read confirms, the text is "Box NAME predates the native runtime and cannot be
started or restarted. It is stopped. Recreate it to continue: yoloctl box
recreate NAME --space SPACE."; a failed or unconfirmed stop says so instead
and never "It is stopped". Other errors are returned unchanged. [4](#source-4) [6](#source-6)

[FACT] `box.inspect` reports `legacy_runtime` from the launcher read. The host
skips the pre-dispatch recreate offer for such a box. After a refused start,
restart or attach (or `box set`/`box reconcile` of a box read running before
it), a person at the terminal is asked once, only when a fresh read shows it
pre-native and stopped: "Recreation deletes files stored only in the
container. Workdir, declared cache and account state are preserved. Recreate
NAME now? [y/N]". Default No, EOF and non-interactive use keep the refusal.
A yes sends one ordinary `box recreate NAME --force --no-attach`; neither it
nor the original request is retried or sent again. [5](#source-5) [6](#source-6)

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
4. <a id="source-4"></a> [cmd/yoloctl/legacy_runtime.go](../../cmd/yoloctl/legacy_runtime.go#L41) — `refuseLegacyRun`; callers in `agentbox.go`, `session_client.go`, `chat_runtime.go`, `managed_applier.go` `Restart`.
5. <a id="source-5"></a> [cmd/yoloctl/recreate_offer.go](../../cmd/yoloctl/recreate_offer.go#L107) — `boxLegacyRecreateOffer`, `offerLegacyRecreate`; wiring in `controller_client.go` `callControllerTerminal`; `inspectIdentity.LegacyRuntime`; `boxstate.Box.LegacyRuntime`; `launcher_linux.go` `BoxLauncher`.
6. <a id="source-6"></a> [cmd/yoloctl/legacy_runtime_test.go](../../cmd/yoloctl/legacy_runtime_test.go#L88) — stop/refusal, unconfirmed stop, policy apply, offer, terminal path, restart-sessions and revival; `chat_runtime_output_test.go` chat server refusal.
7. <a id="source-7"></a> [cmd/yoloctl/controller_sessions.go](../../cmd/yoloctl/controller_sessions.go#L339) — refused legacy restart leg; `setAsideRecreations`; `session_remedy.go` `boxDriftView.LegacyRuntime`.
