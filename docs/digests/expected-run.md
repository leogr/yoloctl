# Expected run per box

Verification: `32f245c3415ce3a1caf7bd1eec2d4b086cf50188` and accompanying changes; re-read cited sources when changing behavior.

[FACT] A run is the full container ID with the run's own start time and PID
from inspect: a stopped container started again keeps its ID but not its start
time. Inspect decodes both from `State` for every runtime.
[1](#source-1) [5](#source-5)

[FACT] Each wrapped operation, chat ensure and chat connect/disconnect keep a
per-box record of the run they expect. The container client records the run it
started (read back by ID) and "none" on its own stop; reads record a run only
when nothing is recorded yet. A box without a record is checked by nothing.
[1](#source-1) [2](#source-2)

[FACT] Before a port forward, the agent version read and install, the session
holder start, each managed chat exec and the chat server reload, the box's
container is read and compared: absent, not running, another ID or a new run
ends the operation with `box NAME stopped during the operation, after its last
start or read, before STEP`, a plain failure with no uncertainty. The read is
not atomic with the effect. An unreadable box before the agent update still
skips it. [2](#source-2) [3](#source-3) [4](#source-4)

[FACT] A native policy restart and a native start of an existing box launch only
the box's saved policy: a known or uncertain launch failure, or a cancellation
after the old runtime stopped, never launches the previous policy again; the
launch's own cleanup ran and the box's state is what a read shows. The legacy
runtime's restart still restores its previous policy when its new launch failed
before its engine start. Engine-call records nest, so box start still sees the
launch's start. [3](#source-3) [4](#source-4)

[FACT] Tests use a fake engine; they are not live VM validation.
[4](#source-4)

## Sources

1. <a id="source-1"></a> [internal/container/expected_runs.go](../../internal/container/expected_runs.go#L1) — `Run`, `ExpectedRuns`, `StoppedDuringOperation`, `CheckExpectedRun`; recording in `Client.Start`, `createOrRun`, `Stop`, `StopContainerID`.
2. <a id="source-2"></a> [cmd/yoloctl/space_operation.go](../../cmd/yoloctl/space_operation.go#L78) — record per wrapped operation; chat records in `chat_runtime.go` `ensureManagedServer`, `chat_participation.go`.
3. <a id="source-3"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L1) — `AddPortForward` check, `LiveInspector.Inspect` read, legacy `Restart`; `openshell_policy_restart_transaction_linux.go` `completeNativePolicyRestart`; `openshell_start.go` `retainNativePreparedStart`.
4. <a id="source-4"></a> [cmd/yoloctl/expected_run_test.go](../../cmd/yoloctl/expected_run_test.go#L1) — agent update, holder start, chat exec; `internal/container/expected_runs_test.go`, agentbox `expected_run_port_test.go`, `openshell_policy_restart_transaction_linux_test.go`, `openshell_prepared_start_test.go`.
5. <a id="source-5"></a> [Run-marker evidence](../../internal/container/container.go#L577) — `Details.PID`, `Details.StartedAt` decoded from `State`.
