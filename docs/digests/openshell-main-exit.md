# Native OpenShell shutdown and main-exit observation

Verification: `6e71f7dff5b841a32da02737b50dc206058ff225` and accompanying changes,
with upstream OpenShell `ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47`; upstream citations re-read at v0.1.10 `152c59d3178039e3fbb4a5595f9fff7216a6be92` (main `f357061bf0f425f6d0a007d6fb31f575f038c827` and accompanying changes).
Shutdown ordering at `f357061bf0f425f6d0a007d6fb31f575f038c827` and accompanying changes.
Activation waits verified at `c33648af` and accompanying changes.

[FACT] On a requested stop of a ready native box with both children alive,
PID 1 sends TERM to the supervisor first, keeping the boundary available for
the supervisor's shutdown protocol. After reaping the supervisor, PID 1
sends TERM to any remaining boundary within the same five-second grace.
One reaper retains both original wait results. At expiry, survivors get
SIGKILL and up to three seconds for reaping. A stop-cancelled readiness
probe keeps the last completed observation. Startup, degradation, errors,
missing children and main-exit cleanup use the parallel TERM route.
Existing boxes use their installed helper until explicit recreation.
[1](#source-1) [11](#source-11) [12](#source-12)

[FACT] Before startup, native activation gives the connection wait and
the authenticated exchange separate two-minute allowances. Both peers
derive the exchange from the original caller, so an earlier caller end
still stops either wait. [13](#source-13) [14](#source-14)

[FACT] Native PID 1 observes `main-exit` through its retained supervisor-run
directory on the existing 100 ms monitor loop. Before starting children it
removes both existing regular markers only after checking supervisor ownership.
The monitor adds no workload predicate: no marker preserves ordinary behavior,
including supervisor-loss degradation. This reader does not enable the
supervisor flag or change workload launch. [1](#source-1) [2](#source-2)

[FACT] The marker is opened without following links or blocking on a FIFO. A
regular supervisor-owned file containing exactly `exit_code=N\n`, with N in
0–255, supplies the main exit. An observed invalid object/content ends the
runtime without a known code; failed observation remains unavailable and is
reported, not converted into a main exit. [2](#source-2) [3](#source-3)

[FACT] A main exit closes new SSH admission, publishes `main-exited`, and follows
normal relay and child cleanup. For a valid code it first keeps existing relays
and children alive until a supervisor-owned, no-follow regular
`main-attachments-ended` containing exactly `ended\n` appears, or one overall
`runtimeMainExitWait` deadline (4 s, kept independent of the endpoint's two-minute close fail-safe: its end fails nothing) expires, whichever comes
first. A missing, invalid or unreadable second marker does not shorten the wait;
an invalid main marker still ends immediately. The second marker reports
attachment completion or abandonment, not delivered output. A healthy slow
holder can hit the bound and lose remaining output. This one deadline does not
observe the privileged endpoint's separate phase boundaries. The final status
retains optional `main_exit`;
its phase distinguishes stopped children from unproved cleanup. The helper
returns the known main code, or 1 for an invalid marker. Cleanup diagnostics
remain reportable. Old status records without the field still validate; the
pure decoder checks the selected record identity and grants no storage or
container authority. [1](#source-1) [2](#source-2) [4](#source-4) [5](#source-5) [9](#source-9) [10](#source-10)

[FACT] The pinned supervisor publishes the marker using a mode-0600 temporary
file, file fsync, rename and parent fsync. Native cleanup admits only `main-exit`
and `main-attachments-ended`, plus only their `.BASENAME.tmp-<PID>` producer names with supervisor ownership and
mode 0600. Marker observation does not establish delivery of final SSH output
or status to a client. [6](#source-6) [7](#source-7) [8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [runtime_main_linux.go](../../internal/openshell/runtime_main_linux.go#L45-L313) — `runRuntimePID1`, `monitorRuntime`, `runtimeStopAfterProbe`: marker decision, stop order, admission, relay and child cleanup.
2. <a id="source-2"></a> [runtime_main_exit_linux.go](../../internal/openshell/runtime_main_exit_linux.go#L1-L185) — descriptor-relative parsing, stale removal, outcome and final observation.
3. <a id="source-3"></a> [runtime_main_exit_linux_test.go](../../internal/openshell/runtime_main_exit_linux_test.go#L1-L302) — format/type/owner, unavailable, exact-run and monitor controls.
4. <a id="source-4"></a> [runtime_status_linux.go](../../internal/openshell/runtime_status_linux.go#L1-L137) — `RuntimeStatus`, `DecodeRuntimeStatus`, phase/code validation.
5. <a id="source-5"></a> [runtime_manifest_linux.go](../../internal/openshell/runtime_manifest_linux.go#L1-L109) — `ProjectedRuntimeMain`, `projectedRuntimeResult`.
6. <a id="source-6"></a> [upstream supervisor lib.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-supervisor/src/lib.rs#L2070-L2118) — `persist_main_exit_marker`, `write_private_file_atomically`, main-exit atomic publication; the local attachment-ended extension is integrated separately.
7. <a id="source-7"></a> [reservation_cleanup_linux.go](../../internal/openshell/reservation_cleanup_linux.go#L296-L460) — `runtimeReservationMember`, inventory ownership/mode checks; producer-name allowlist and inactive inventory.
8. <a id="source-8"></a> [cleanup fixture](../../internal/openshell/reservation_runtime_cleanup_linux_test.go#L48-L80) — regular marker and temporary members survive strict inventory and are removed only in acknowledged cleanup.

9. <a id="source-9"></a> [ordering controls](../../internal/openshell/runtime_main_exit_ordering_linux_test.go#L1-L147) — fake-clock deadline/early end, invalid or unavailable completion evidence, stale markers and narrow cleanup inventory.
10. <a id="source-10"></a> [endpoint](../../internal/boxendpoint/server_linux.go#L20-L50) — `DefaultCloseTimeout`, the endpoint's two-minute close fail-safe; the ordering test keeps the native 4 s drain independent of it.
11. <a id="source-11"></a> [Child signalling and reaping](../../internal/openshell/runtime_process_linux.go#L60-L172) — exact waits, graceful order and forced cleanup.
12. <a id="source-12"></a> [Existing helper pin](../../internal/openshell/helper_pins.go#L95-L113) — previously installed helper remains in use.
13. <a id="source-13"></a> [Native client connection](../../internal/openshell/activation_client_linux.go#L58-L82) — `activate` hands its original context to the exchange.
14. <a id="source-14"></a> [Shared activation wait](../../internal/openshell/activation_server_linux.go#L162-L191) — `acceptNativeActivation`, `beginActivationExchange`.
