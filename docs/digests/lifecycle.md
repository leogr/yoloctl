# Reconcile, native locks and space lifecycle

Verification: `4bc09137374e91d374ba6454cd8cef3d3da44240` and accompanying changes (installer consent removal); other facts at `1d265df3`; re-read cited sources when changing behavior.

[FACT] Explicit reconcile selects create, recreate, restart, then live port changes. Account,
agent, mounts/workdir access, image, literal env and network mode can require
recreation; operator infrastructure changes also recreate. Other policy changes
restart. Extra agent args affect the next session. The live inspector reports
normalized CPU/memory limits; changing or removing caps recreates, while equivalent
units converge. Old containers recreate to join the workload memory pool.
[1](#source-1) [2](#source-2)

[FACT] Primary workdir inspection uses the recorded sandbox `--workdir` from
`Path`/`Args`, independent of desired paths and mount order. Other `/workdir`
binds remain extras; moved paths and access changes still reconcile. Missing
or ambiguous metadata aborts inspection before mutation.
[2](#source-2) [22](#source-22) [23](#source-23) [24](#source-24)

[FACT] Start on a running container is a no-op. Other starts/restarts refuse
recreate-only drift with inspect/recreate guidance before runtime changes,
and check again afterward. Post-start reconcile failures return errors while
retaining the container. Missing containers still materialize; policy restarts and port updates
retain managed cleanup. Explicit reconcile/recreate retains replacement authority.
[25](#source-25) [26](#source-26) [27](#source-27)

[FACT] Managed appliers clean session holders/artifacts before restart/recreate;
a cleanup that positively stopped the holder and removed its state then lets a
managed reconcile record the launch's end before the container operation.
Stop removes forwards and session state and reaps orphan netns. Socket readiness
requires an SSH banner, not just an inode, and reports a confirmed container exit
with a logs hint. Native commands use the local sandbox SSH launch path.
[3](#source-3) [4](#source-4)

[FACT] `box destroy` requires an authoritative runtime inventory before holder,
container or declaration cleanup. An unavailable space or failed container listing
aborts, including with `--force` or `--keep-manifest`; it cannot become successful
declaration-only deletion. A confirmed absent container still permits normal
cleanup. Regression tests inject failed and partial inventories and verify that
declaration bytes remain unchanged. [20](#source-20) [21](#source-21)

[FACT] Graceful stop requests `kill -TERM -1` through private OpenShell SSH as
the confined user without a PTY, leaving PID 1 to reap and clean up. Its
three-second budget precedes `nerdctl stop --time 30` and SIGKILL escalation.
Modern runc's obsolete `--all` cannot broadcast SIGTERM. The isolated smoke
checks exit 143, repeated stop/start, cached recreate and peer survival.
[15](#source-15) [16](#source-16) [17](#source-17)
[18](#source-18) [19](#source-19)

[FACT] Host boot/adapter locks are separate from native space-before-entity
locks; none cross the host/guest filesystem boundary. Builds/npm updates serialize
setup; readers/existing-holder attaches bypass it. Streaming/editor waits release
it. Editor saves reject changed bytes, file identity, catalogs, defaults or
generation. Lock waits are cancelable with feedback. Cross-space copies snapshot
before destination mutation.
[5](#source-5) [6](#source-6) [7](#source-7)

[FACT] Host lifecycle explicitly boots a space and deploys its selected controller
bundle. Ordinary operations require it running. Mount changes require a running
controller, reject revoked grants used by valid declarations, confirm a restart,
drain, recheck dependents and publish new boot settings/generation. Declining
changes no declaration or host directory. Box starts remain explicit afterward.
[8](#source-8) [9](#source-9)

[FACT] Stop persists controller maintenance and drains setup, performs
session-aware cleanup with up to eight boxes stopping concurrently, then stops
the selected VM adapter. Per-box holder/port/container/state ordering is preserved and orphan netns
are reaped once afterward. The two-minute graceful budget reserves 30 seconds
for VM poweroff after draining/cleanup; failure uses a bounded forced stop.
[14](#source-14) Runtime deletion verifies termination
before guarded forced metadata removal. `recreate` preserves the data disk and
native declarations/accounts but loses system-disk images, containers, sessions
and ephemeral workdirs. `destroy` deletes VM, owned data disk, then host bundle;
failures retain recovery metadata. Host projects are never removed.
[8](#source-8) [10](#source-10) [11](#source-11)

[FACT] Host-only export/import preserves declarations/native accounts in versioned
archives. Import creates fresh ownership/disk identity, supports backend selection
and leaves boxes stopped. Schema-2 migration is removed.
[8](#source-8) [12](#source-12) [13](#source-13)

[FACT] Controller restart preserves session units. Interrupted mutations may
resolve from durable evidence or an allowlisted read-only proof; unproved
outcomes retain dependency holds for host review. Recovery never replays a
command. [28](#source-28) Unit/publication and isolated archive tests cover recovery,
export/import/recreate, not physical power-loss behavior.
[7](#source-7) [12](#source-12) [13](#source-13)

## Sources

1. <a id="source-1"></a> [internal/agentbox/diff.go](../../internal/agentbox/diff.go#L163) — Diff and recreateReason.
2. <a id="source-2"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L66) — `Inspect` and `liveWorkdirMount`, lines 66–211.
3. <a id="source-3"></a> [cmd/yoloctl/managed_applier.go](../../cmd/yoloctl/managed_applier.go#L23) — managed session cleanup.
4. <a id="source-4"></a> [internal/agentbox/readiness.go](../../internal/agentbox/readiness.go#L39) — SSH readiness and container exit probe.
5. <a id="source-5"></a> [cmd/yoloctl/space_operation.go](../../cmd/yoloctl/space_operation.go#L29) — native setup locks and streaming release.
6. <a id="source-6"></a> [internal/spacelock/lock.go](../../internal/spacelock/lock.go#L16) — stable lock, wait notification and cancellation.
7. <a id="source-7"></a> [Controller evidence](controller.md) — transport, editor revisions, receipt recovery and drain.
8. <a id="source-8"></a> [cmd/yoloctl/controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L72-L205) — create/start/recreate/destroy orchestration.
9. <a id="source-9"></a> [cmd/yoloctl/controller_mounts.go](../../cmd/yoloctl/controller_mounts.go#L36) — dependent checks and mount lifecycle.
10. <a id="source-10"></a> [cmd/yoloctl/controller_deploy.go](../../cmd/yoloctl/controller_deploy.go#L336-L369) — drain and bounded VM stop.
11. <a id="source-11"></a> [cmd/yoloctl/space_lifecycle.go](../../cmd/yoloctl/space_lifecycle.go#L221) — verified runtime deletion.
12. <a id="source-12"></a> [internal/controller/store.go](../../internal/controller/store.go#L147) — staged publication, checksums and activation.
13. <a id="source-13"></a> [Archive lifecycle](../../cmd/yoloctl/space_archive.go#L1) — export/import and recovery.

14. <a id="source-14"></a> [cmd/yoloctl/space_lifecycle.go](../../cmd/yoloctl/space_lifecycle.go#L91) — stopSpaceBoxes; concurrent grace and one reaper.
15. <a id="source-15"></a> [internal/sandbox/stop.go](../../internal/sandbox/stop.go#L23) — bounded confined signalling and runtime fallback.
16. <a id="source-16"></a> [runc v1.3.4 kill](https://github.com/opencontainers/runc/blob/v1.3.4/kill.go#L45) — obsolete all flag and init-only signal.
17. <a id="source-17"></a> [scripts/smoke/box-stop.sh](../../scripts/smoke/box-stop.sh#L1) — isolated graceful shutdown acceptance.
18. <a id="source-18"></a> [internal/sandbox/stop_test.go](../../internal/sandbox/stop_test.go#L18) — SSH request, timeout and fallback tests.
19. <a id="source-19"></a> [OpenShell v0.0.72 process runner](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-process/src/run.rs) — supervised entrypoint wait/cleanup.
20. <a id="source-20"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L2134) — `newAgentboxDestroyCmd` requires `RuntimeInventory` before teardown.
21. <a id="source-21"></a> [cmd/yoloctl/agentbox_test.go](../../cmd/yoloctl/agentbox_test.go#L30) — `TestAgentboxDestroyUncertainRuntimePreservesDeclaration`: inventory failures, force/keep-manifest combinations.
22. <a id="source-22"></a> [Inspector regressions](../../internal/agentbox/live_inspect_test.go#L20) — convergence, drift and refusal.
23. <a id="source-23"></a> [parseInspect](../../internal/container/container.go#L568) — runtime argv decoding.
24. <a id="source-24"></a> [nerdctl v2.3.5](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/inspecttypes/dockercompat/dockercompat.go#L339) — `Path`/`Args` from OCI argv.
25. <a id="source-25"></a> [reconcile.go](../../internal/agentbox/reconcile.go#L91) — `ReconcileForStart`, `CheckStart`, shared planning.
26. <a id="source-26"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L664) — `startAgentbox`, `preflightAgentboxRestartWithIntent`, `newAgentboxRestartCmd`.
27. <a id="source-27"></a> [Command regressions](../../cmd/yoloctl/agentbox_start_test.go#L24) — early/late drift refusal and teardown ordering.

28. <a id="source-28"></a> [Interrupted-outcome recovery](controller-recovery.md) — durable proofs, dependency holds and storage reconciliation.
