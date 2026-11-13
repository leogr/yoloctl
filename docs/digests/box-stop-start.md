# Box stop and start results

Verification: `19fe5134273d38642397a288132586b4e94dcd10` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `box stop` runs each step once, in order: read the box's exact container;
withdraw the session holder; withdraw the box's forwards; one engine stop when the
read saw it running (on `unconfined_network` the namespace retirement performs it);
on the native route remove the endpoint socket; clean the session state; on the
generic route reap orphan network namespaces; read again. A failing step is
recorded and the next one runs. The result comes from the last read only: not
running or absent is `box NAME stopped` (exit 0) plus one warning per leftover;
running is `stop failed: box NAME is still running` with the step notes. A failed
first read changes nothing; a failed last read fails the command and never says
stopped. No step's error, including a typed uncertainty marker, leaves it.
[1](#source-1) [2](#source-2) [6](#source-6)

[FACT] The result mode's engine stop keeps the native ownership checks but drops
the precondition that no forward is listed and does not remove the endpoint.
The endpoint socket is removed by its own step only when the directory, opened
without following links, holds a socket, the exact container reads not running,
no forward of the box is listed, the reopened directory is the same file, and the
socket is the protected runtime socket. Otherwise it is kept and named; with no
container it is kept for recreate or destroy. Generation directories of earlier
starts are retained. [3](#source-3) [7](#source-7)

[FACT] `box start` of a stopped container first removes the holder unit that the
holder inventory lists and, on the native route, the endpoint socket under the
same checks; one it cannot remove fails the start before any engine start. After
the start helpers mark an engine start as submitted, any later failure, or a
final read that is not running, is rolled back with the `box stop` steps:
`start failed: REASON`, plus `; box NAME is still running` when the rollback's
read still sees it running. Before submission the start's own error stands,
including recreate-only drift refusals. [1](#source-1) [4](#source-4) [6](#source-6) [8](#source-8)

[FACT] Restart, recreate, destroy, account and space teardown keep today's
native stop with its forward precondition, endpoint removal and early returns;
only the `box start` command passes the result policy. [4](#source-4) [5](#source-5) [8](#source-8)

[FACT] A `box.start` or `box.stop` receipt that ended without a result after its
effects were authorized (not setup-complete, scoped, no image writes) is
classified `interrupted` at server finalization and at recovery. It is final,
evictable, holds nothing and needs no review; its caller gets `box stop did not
finish; run "box list" to read the box's state`. Recorded-completion and
not-applied proofs keep precedence; pending receipts still reserve; every other
method, including restart and recreate, keeps `uncertain`.
[9](#source-9) [10](#source-10) [11](#source-11)

[FACT] Tests stub the engine and steps; they are not live VM validation.
Admission of `box stop` under another operation's hold is unchanged.
[6](#source-6) [7](#source-7) [11](#source-11)

## Sources

1. <a id="source-1"></a> [cmd/yoloctl/box_stop_result.go](../../cmd/yoloctl/box_stop_result.go#L40-L219) — `liveBoxStopSteps`, `stopAgentboxForResult`, `runBoxStop`, `removeStartLeftovers`, `settleStartForResult`.
2. <a id="source-2"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L2250) — `box stop` RunE calls `runBoxStop`.
3. <a id="source-3"></a> [internal/agentbox/openshell_stop_linux.go](../../internal/agentbox/openshell_stop_linux.go#L24-L158) — `stopNativeRuntimeForResult`, `stopNativeRuntimeMode`; `RemoveStoppedEndpoint` and `removeStoppedEndpoint` at L221-L300.
4. <a id="source-4"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L735-L900) — `startAgentboxWithForwardIntent` `resultPolicy`; `markStartSubmitted` in `startExistingAgentboxContainerPrepared`.
5. <a id="source-5"></a> [internal/agentbox/openshell_stop.go](../../internal/agentbox/openshell_stop.go#L13-L42) — `Stop` (today's mode) and `StopRuntimeForResult`.
6. <a id="source-6"></a> [cmd/yoloctl/box_stop_result_test.go](../../cmd/yoloctl/box_stop_result_test.go#L46) — stop step failures, lost replies, reads, absent/exited, start leftovers and rollback.
7. <a id="source-7"></a> [internal/agentbox/openshell_stop_endpoint_linux_test.go](../../internal/agentbox/openshell_stop_endpoint_linux_test.go#L52) — endpoint kept unless every check holds.
8. <a id="source-8"></a> [cmd/yoloctl/agentbox_start_test.go](../../cmd/yoloctl/agentbox_start_test.go#L24) — `TestBoxStartAndRestartPreserveExistingContainer`: start rollback, restart unchanged.
9. <a id="source-9"></a> [internal/controller/receipts.go](../../internal/controller/receipts.go#L76-L180) — `authorizedStartStop`, `interrupt`, `terminal`.
10. <a id="source-10"></a> [internal/controller/server.go](../../internal/controller/server.go#L1096) — finalization exit state; `InterruptedMessage` in client.go; `explainOutcome` in holds.go.
11. <a id="source-11"></a> [internal/controller/interrupted_test.go](../../internal/controller/interrupted_test.go#L20) — classification, holds and reservations.
