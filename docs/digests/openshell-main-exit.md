# Native OpenShell main-exit observation

Verification: `549d46495e13d1814aff3358c3a68fc7463211be` and accompanying changes;
upstream OpenShell `ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47`.

[FACT] Native PID 1 observes `main-exit` through its retained supervisor-run
directory on the existing 100 ms monitor loop. Before starting children it
removes an existing regular marker only after checking supervisor ownership.
The monitor adds no workload predicate: no marker preserves ordinary behavior,
including supervisor-loss degradation. This reader does not enable the
supervisor flag or change workload launch. [1](#source-1) [2](#source-2)

[FACT] The marker is opened without following links or blocking on a FIFO. A
regular supervisor-owned file containing exactly `exit_code=N\n`, with N in
0–255, supplies the main exit. An observed invalid object/content ends the
runtime without a known code; failed observation remains unavailable and is
reported, not converted into a main exit. [2](#source-2) [3](#source-3)

[FACT] A main exit closes new SSH admission, publishes `main-exited`, and follows
normal relay and child cleanup. The final status retains optional `main_exit`;
its phase distinguishes stopped children from unproved cleanup. The helper
returns the known main code, or 1 for an invalid marker. Cleanup diagnostics
remain reportable. Old status records without the field still validate; the
pure decoder checks the selected record identity and grants no storage or
container authority. [1](#source-1) [2](#source-2) [4](#source-4) [5](#source-5)

[FACT] The pinned supervisor publishes the marker using a mode-0600 temporary
file, file fsync, rename and parent fsync. Native cleanup admits only `main-exit`
and its `.main-exit.tmp-<PID>` producer names with supervisor ownership and
mode 0600. Marker observation does not establish delivery of final SSH output
or status to a client. [6](#source-6) [7](#source-7) [8](#source-8)

## Sources

1. <a id="source-1"></a> [runtime_main_linux.go](../../internal/openshell/runtime_main_linux.go#L1-L264) — `runRuntimePID1`, `monitorRuntime`: marker decision, admission, relay and child cleanup.
2. <a id="source-2"></a> [runtime_main_exit_linux.go](../../internal/openshell/runtime_main_exit_linux.go#L1-L142) — descriptor-relative parsing, stale removal, outcome and final observation.
3. <a id="source-3"></a> [runtime_main_exit_linux_test.go](../../internal/openshell/runtime_main_exit_linux_test.go#L1-L285) — format/type/owner, unavailable, exact-run and monitor controls.
4. <a id="source-4"></a> [runtime_status_linux.go](../../internal/openshell/runtime_status_linux.go#L1-L137) — `RuntimeStatus`, `DecodeRuntimeStatus`, phase/code validation.
5. <a id="source-5"></a> [runtime_manifest_linux.go](../../internal/openshell/runtime_manifest_linux.go#L1-L109) — `ProjectedRuntimeMain`, `projectedRuntimeResult`.
6. <a id="source-6"></a> [pinned supervisor lib.rs](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-supervisor/src/lib.rs) — `persist_main_exit_marker`, `write_private_file_atomically`, main-exit publication before attachment wait/status release.
7. <a id="source-7"></a> [reservation_cleanup_linux.go](../../internal/openshell/reservation_cleanup_linux.go#L296-L460) — `runtimeReservationMember`, inventory ownership/mode checks; producer-name allowlist and inactive inventory.
8. <a id="source-8"></a> [cleanup fixture](../../internal/openshell/reservation_runtime_cleanup_linux_test.go#L48-L80) — regular marker and temporary members survive strict inventory and are removed only in acknowledged cleanup.
