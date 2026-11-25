# Reconcile, native locks and space lifecycle

Verification: `0bfec5a3ac693f226a04d280634025bf35d66e1a` and accompanying changes (saved-declaration errors); `4bc09137374e91d374ba6454cd8cef3d3da44240` and accompanying changes (installer consent removal); exact inspect selection at `820e87cf0fdec4c48c82d51eb2a8296504b61b01` and accompanying changes; runtime target selection at `9169119ca6b27e530064dc49b8b7a1d06c17397b` and accompanying changes; box start/stop results at `19fe5134273d38642397a288132586b4e94dcd10` and accompanying changes; other facts at `1d265df3`; re-read cited sources when changing behavior; holding and source symbols re-read at `eddfb42a14f31c9f8d85069f2a861931367da86d`; legacy DNAT PATH at `f3a4aa1` and accompanying changes. nerdctl inspect sources re-read at `fcd4a112` and accompanying changes.

[FACT] Explicit reconcile selects create, recreate, restart, then live port changes. Account,
agent, mounts/workdir access, image, literal env and network mode can require
recreation; operator infrastructure changes also recreate. Other policy changes
restart. Extra agent args affect the next session. The live inspector reports
normalized CPU/memory limits; changing or removing caps recreates, while equivalent
units converge. Old containers recreate to join the workload memory pool.
[1](#source-1) [2](#source-2)

[FACT] Reconcile detects removed managed environment keys by looking them up
in the desired environment map. A container without the managed-key label
uses the declared-key check and skips removal detection. [37](#source-37)

[FACT] When reconciliation fails after `box set` or `box edit` saved the
declaration, a user error says it is saved and applying it failed, preserving
the cause
and naming `yoloctl box reconcile NAME --space SPACE` to retry after inspecting
current state and fixing the cause. Partial effects are possible. Removing a
permission adds a warning that the previous container may retain it until
reconcile, recreate or stop succeeds. Save/validation failures before that boundary do not claim a saved change. No automatic retry
or rollback is added. [33](#source-33) [34](#source-34) [35](#source-35) [36](#source-36)

[FACT] Primary workdir inspection uses the recorded sandbox `--workdir` from
`Path`/`Args`, independent of desired paths and mount order. Other `/workdir`
binds remain extras; moved paths and access changes still reconcile. Missing
or ambiguous metadata aborts inspection before mutation. Inspect keeps only
the entry whose recorded name or full ID equals the request (nerdctl also
returns ID-prefix matches): none is "no such container", several an error.
[2](#source-2) [22](#source-22) [23](#source-23) [24](#source-24) [29](#source-29)

[FACT] Ordinary start/stop/remove and maintenance/legacy-DNAT execs use the
exactly selected full canonical container ID, avoiding hex-name/ID-prefix
collisions. Already resolved IDs stay fixed; a failed CID command does not fall
back to name selection. Names still identify sockets and operator cleanup. Scoped native ID/receipt
paths and confined SSH exec stay unchanged; raw log selectors are not covered.
Legacy DNAT add and delete invoke `iptables` with the fixed system PATH.
[30](#source-30) [31](#source-31) [32](#source-32)

[FACT] Start on a running container is a no-op. Other starts/restarts refuse
recreate-only drift with inspect/recreate guidance before runtime changes,
and check again afterward. Post-start restart failures return errors while
retaining the container; `box start` rolls a submitted start back with `box stop`'s
steps and decides from a read ([box stop and start](box-stop-start.md)). Missing containers still materialize; policy restarts and port updates
retain managed cleanup. Explicit reconcile/recreate retains replacement authority.
[25](#source-25) [26](#source-26) [27](#source-27)

[FACT] Managed appliers clean session holders/artifacts before restart/recreate;
a cleanup that positively stopped the holder and removed its state then lets a
managed reconcile record the launch's end before the container operation.
`box stop` takes its result from a final read and, when another operation holds
the space lock, stops beside it, as [box stop and start](box-stop-start.md) says. Socket readiness
requires an SSH banner, not just an inode, and reports a confirmed container exit
with a logs hint. Native commands use the local sandbox SSH launch path.
[3](#source-3) [4](#source-4)

[FACT] `box destroy` requires an authoritative runtime inventory before holder,
container or declaration cleanup. An unavailable space or failed container listing
aborts, including with `--force` or `--keep-manifest`; it cannot become successful
declaration-only deletion. A confirmed absent container still permits normal
cleanup; failed and partial inventories are regression-tested to leave
declaration bytes unchanged. [20](#source-20) [21](#source-21)

[FACT] Graceful stop requests `kill -TERM -1` through private OpenShell SSH as
the confined user without a PTY, leaving PID 1 to reap and clean up. Its
three-second budget precedes `nerdctl stop --time 30` and SIGKILL escalation.
Modern runc's obsolete `--all` cannot broadcast SIGTERM. Smoke checks cover
exit 143, repeated stop/start, cached recreate and peer survival.
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
resolve from durable evidence or an allowlisted read-only proof; an unproved
outcome is closed as `interrupted` and holds nothing. Recovery never replays a
command. [28](#source-28) Tests cover recovery and export/import/recreate, not
physical power loss.
[7](#source-7) [12](#source-12) [13](#source-13)

## Sources

1. <a id="source-1"></a> [internal/agentbox/diff.go](../../internal/agentbox/diff.go#L163) — Diff and recreateReason.
2. <a id="source-2"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L66) — `Inspect` and `liveWorkdirMount`, lines 66–211.
3. <a id="source-3"></a> [cmd/yoloctl/managed_applier.go](../../cmd/yoloctl/managed_applier.go#L23) — managed session cleanup.
4. <a id="source-4"></a> [internal/agentbox/readiness.go](../../internal/agentbox/readiness.go#L39) — SSH readiness and container exit probe.
5. <a id="source-5"></a> [cmd/yoloctl/space_operation.go](../../cmd/yoloctl/space_operation.go#L29) — native setup locks and streaming release.
6. <a id="source-6"></a> [internal/spacelock/lock.go](../../internal/spacelock/lock.go#L16) — stable lock, wait notification and cancellation.
7. <a id="source-7"></a> [Controller evidence](controller.md) — transport, editor revisions, receipt recovery and drain.
8. <a id="source-8"></a> [cmd/yoloctl/controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L71-L267) — create/start/recreate/destroy orchestration.
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
23. <a id="source-23"></a> [Inspect selection](../../internal/container/container.go#L615-L726) — `parseContainerIP`, `parseInspect`, `selectInspectEntry`;
    [tests](../../internal/container/inspect_select_test.go#L1-L99).
24. <a id="source-24"></a> [nerdctl v2.4.2](https://github.com/containerd/nerdctl/blob/v2.4.2/pkg/inspecttypes/dockercompat/dockercompat.go#L375-L384) — `Path`/`Args` from OCI argv.
25. <a id="source-25"></a> [reconcile.go](../../internal/agentbox/reconcile.go#L91) — `ReconcileForStart`, `CheckStart`, shared planning.
26. <a id="source-26"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L664) — `startAgentbox`, `preflightAgentboxRestart`, `newAgentboxRestartCmd`.
27. <a id="source-27"></a> [Command regressions](../../cmd/yoloctl/agentbox_start_test.go#L24) — early/late drift refusal and teardown ordering.

28. <a id="source-28"></a> [Interrupted-outcome recovery](controller-recovery.md) — durable proofs, interrupted closing without holds and deferred records.
29. <a id="source-29"></a> [nerdctl v2.4.2 container walker](https://github.com/containerd/nerdctl/blob/v2.4.2/pkg/idutil/containerwalker/containerwalker.go#L47-L74) — `Walk` matches the name label or an ID prefix; `container inspect` returns every match.
30. <a id="source-30"></a> [Runtime target](../../internal/container/target.go#L1) — `ValidateContainerID`, `ResolveContainerID`; `Client.Start/Stop/Remove` in container.go; collision/refusal/fixed-ID/error controls in target_test.go; named SSH with retained CID in sandbox/stop.go `StopContainerID`.
31. <a id="source-31"></a> [Maintenance](../../cmd/yoloctl/autoupdate.go#L97) — npm/version helpers; native install in agent_distribution.go and process probe in agent.go; container_target_test.go covers target argv and refusals.
32. <a id="source-32"></a> [Legacy forwarding](../../internal/port/port.go#L94-L172) — inspected bridge/CID and `dnatExecArgs`; `legacyDNATTarget` verifies cleanup routing, and `TestLegacyDNATUsesSystemPathAndExactSelectedID` checks argv, mode and refusals.

33. <a id="source-33"></a> [Saved apply errors](../../cmd/yoloctl/privileged_operator.go#L192) — `savedDeclarationError`.
34. <a id="source-34"></a> [Set/edit apply tail](../../cmd/yoloctl/agentbox_reconcile.go#L766) — `reconcileAndPrint`.
35. <a id="source-35"></a> [Controller edit apply](../../cmd/yoloctl/controller_documents.go#L250) — saved declaration enters the shared reconcile tail.

36. <a id="source-36"></a> [Declaration permission comparison](../../cmd/yoloctl/saved_permissions.go#L16) — immutable prior snapshot and removal warning, not runtime proof.

37. <a id="source-37"></a> [Environment drift](../../internal/agentbox/diff.go#L388) — `envRecreateReason`; verified at `ed0f79a6f4e9e03e8c0777ec612b58315cbf86b1` and accompanying changes.
