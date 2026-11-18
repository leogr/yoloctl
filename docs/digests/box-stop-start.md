# Box stop and start results

Verification: `54e0df18de728fadc88d0fae984ece2cee237083` and accompanying changes; result-policy users, storage failure and source symbols re-read at `eddfb42a14f31c9f8d85069f2a861931367da86d`; re-read cited sources when changing behavior.

[FACT] `box stop` runs each step once, in order: read the box's exact container;
withdraw the session holder; withdraw the box's forwards; one engine stop when the
read saw it running (on `unconfined_network` the namespace retirement performs it);
on the native route remove the endpoint socket; clean the session state; on the
generic route reap orphan network namespaces; read again. A failing step is
recorded and the next one runs. The result comes from the last read only: not
running or absent is `box NAME stopped` (exit 0) plus one warning per leftover;
running is `stop failed: box NAME is still running` with the step notes. A failed
first read changes nothing; a failed last read fails the command. No step's error, including a typed uncertainty marker, leaves it.
[1](#source-1) [2](#source-2) [6](#source-6)

[FACT] The engine stop targets the full ID of the first read: it inspects that ID,
never the name, requires the box and space labels, and stops nothing when the
ID now belongs to another box or no longer runs; a replacement is never stopped.
It keeps the native ownership checks but drops the precondition that no forward
is listed and does not remove the endpoint. Storage verification runs at its usual
points; a failure before the engine stop stops the container, still the box's by
its engine labels, once by ID and reports the runtime-record checks or, on
`unconfined_network`, the namespace retirement as a leftover; one after it is a
leftover and stops nothing again. [3](#source-3) [5](#source-5) [12](#source-12)

[FACT] The endpoint socket is removed by its own step after its own storage
verification, and only when the directory, opened
without following links, holds a socket, the exact container reads not running,
no forward of the box is listed, the reopened directory is the same file, and the
socket is the protected runtime socket. Otherwise it is kept and named; with no
container it is kept for recreate or destroy. Generation directories of earlier
starts are retained. [3](#source-3) [7](#source-7)

[FACT] `box start` of a stopped container first removes the holder unit that the
holder inventory lists and, on the native route, the endpoint socket under the
same checks; one it cannot remove fails the start before any engine start. The
container client records the engine starts and stops of the attempt (engine
start, activation, readiness, session start and autoupdate, for a stopped or a
missing container) right before invoking the engine; a stop counts only after
the latest start. After an engine start, any failure, or a final read that is not running,
is rolled back with the `box stop` steps, which make no engine stop when the
start's own cleanup already invoked one: `start failed: REASON` with the
rollback's leftovers, plus `; box NAME is still running` when its read still sees
it running. Without an engine start the start's own error stands, including
recreate-only drift and pre-engine native refusals.
[1](#source-1) [4](#source-4) [6](#source-6) [8](#source-8) [13](#source-13)

[FACT] Restart, recreate, destroy, account and space teardown keep today's
native stop with its forward precondition, endpoint removal and early returns.
The `box start` command passes the result policy; the one other user is a
recreate accepted from the pre-native recreation offer (hidden
`--legacy-remedy`), whose failure after the replacement started stops only that
exact run through `runStartForResult`. [4](#source-4) [5](#source-5) [8](#source-8)

[FACT] A `box.start` or `box.stop` receipt that ended without a result after its
effects were authorized (not setup-complete, scoped, no image writes) is
classified `interrupted` at server finalization and at recovery. It is final,
evictable, holds nothing and needs no review; its caller gets an interrupted
failure with a closing observation or state-read advice. Recorded-completion and
live not-applied evidence keeps precedence; stored waiting history after
restart closes interrupted. Live pending admissions still reserve. The shared
interruption rule now applies to every operation, including restart and recreate.
[9](#source-9) [10](#source-10) [11](#source-11)

[FACT] `box stop` tries the space lock once. Free: the steps above. Held
(contention only; other lock failures fail): a branch revalidates an operator,
gates the named box only, selects the box's container once (T), stops T by ID
if running, reads the box again and records its completion, with no cleanup or
role sync. Absent, T or a replacement not running: stopped ("was not running" if T
already was), leftovers named;
running: failure saying "this stop targeted container T". Engine errors stay
diagnostics; a failed read fails. The `box.stop` gate never waits for running
executions and is admitted during drain; ended outcomes hold nothing. A
receipt-storage failure keeps the record in controller memory and never
refuses the stop (`persistExecution`, `deferExecution` in live_receipts.go). [14](#source-14) [15](#source-15) [16](#source-16)

[FACT] Tests stub the engine; they are not live VM validation.
[6](#source-6) [7](#source-7) [11](#source-11)

## Sources

1. <a id="source-1"></a> [cmd/yoloctl/box_stop_result.go](../../cmd/yoloctl/box_stop_result.go#L40-L237) — `liveBoxStopSteps`, `stopAgentboxForResult`, `runBoxStop`, `removeStartLeftovers`, `runStartForResult`, `settleStartForResult`.
2. <a id="source-2"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L2250) — `box stop` RunE calls `runBoxStop`.
3. <a id="source-3"></a> [internal/agentbox/openshell_stop_linux.go](../../internal/agentbox/openshell_stop_linux.go#L24-L280) — `stopNativeRuntimeForResult`, `stopForResultWithStorageFallback`, `stopExactContainerWithoutStorage`, `stopNativeRuntimeMode`; `RemoveStoppedEndpoint` and `removeStoppedEndpoint` at L321-L401.
4. <a id="source-4"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L745-L920) — `startAgentboxCore` `resultPolicy`; `startExistingAgentboxContainerPrepared`; the recreate's `legacyRemedyFlag` branch (legacy_runtime.go L132).
5. <a id="source-5"></a> [internal/agentbox/openshell_stop.go](../../internal/agentbox/openshell_stop.go#L13-L70) — `Stop` (today's mode), `StopRuntimeForResult` and `ResultLeftover`.
6. <a id="source-6"></a> [cmd/yoloctl/box_stop_result_test.go](../../cmd/yoloctl/box_stop_result_test.go#L46) — stop step failures, lost replies, reads, absent/exited, start leftovers and rollback.
7. <a id="source-7"></a> [internal/agentbox/openshell_stop_endpoint_linux_test.go](../../internal/agentbox/openshell_stop_endpoint_linux_test.go#L52) — endpoint kept unless every check holds.
8. <a id="source-8"></a> [cmd/yoloctl/agentbox_start_test.go](../../cmd/yoloctl/agentbox_start_test.go#L24) — `TestBoxStartAndRestartPreserveExistingContainer`: start rollback, restart unchanged.
9. <a id="source-9"></a> [internal/controller/receipts.go](../../internal/controller/receipts.go#L76-L180) — `interrupt`, `terminal`.
10. <a id="source-10"></a> [internal/controller/server.go](../../internal/controller/server.go#L1096) — finalization exit state; interruption text in observation.go; `explainOutcome` in holds.go.
11. <a id="source-11"></a> [internal/controller/interrupted_test.go](../../internal/controller/interrupted_test.go#L20) — classification, holds and reservations.
12. <a id="source-12"></a> [internal/agentbox/openshell_stop_result_test.go](../../internal/agentbox/openshell_stop_result_test.go#L65) — replacement never targeted; `openshell_stop_result_linux_test.go` storage failure and transaction accounting.
13. <a id="source-13"></a> [internal/container/engine_calls.go](../../internal/container/engine_calls.go#L1-L71) — `EngineCalls`, marked in `Client.Start`, `createOrRun`, `Stop`, `StopContainerID`.
14. <a id="source-14"></a> [cmd/yoloctl/box_stop_beside.go](../../cmd/yoloctl/box_stop_beside.go#L1) — `stopBoxBeside`, `stopTargetBeside`; wired in `registerSpaceOperations`.
15. <a id="source-15"></a> [internal/controller/server.go](../../internal/controller/server.go#L186) — `Operation.Beside` in admission and the effect gate; `holds.unresolvedOnly`.
16. <a id="source-16"></a> [cmd/yoloctl/box_stop_beside_test.go](../../cmd/yoloctl/box_stop_beside_test.go#L1) — results, wrapper; controller `beside_test.go`.
