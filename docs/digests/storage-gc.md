# Automatic VM storage cleanup

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Lima deployment and Firecracker guest boot configure the rootful BuildKit containerd worker
in namespace `default`. GC targets 10% of root capacity, capped at 4 GiB,
reserves up to 256 MiB of cache and targets 20% free space. These are collection
targets, not image/container quotas. BuildKit 0.17+ is required; unknown versions
and unexpected workers are rejected. `all=true` permits discarding unused cache
shared with images, so rebuilds may lose cache hits. Containerd image/container
references and active builds remain protected. [1](#source-1) [2](#source-2)
[12](#source-12) [23](#source-23)

[FACT] Deployment preserves unrelated TOML settings, atomically publishes policy,
restarts BuildKit and probes its worker before recording an applied hash covering
TOML and the workload-memory slice/drop-in. Failed activation restores the old
files before restarting again. An absent or mismatched marker forces activation;
unchanged, acknowledged settings do not.
The first rewrite normalizes formatting/comments. Healthy-controller starts skip
deployment. Lima 2.0+ is therefore a prerequisite: its boot script preserves an
existing configuration, while Lima 1.2 overwrites it. [3](#source-3)
[4](#source-4) [13](#source-13) [24](#source-24) [28](#source-28)

[FACT] Cleanup never removes box files. `space storage` and space view report
each box's writable layer, images and BuildKit cache on the system disk; see
[VM disks](space-view-disks.md).

[FACT] Firecracker writes the shared policy before guest readiness and starts
BuildKit on demand. Cleanup skips cache pruning while its socket is absent.
[32](#source-32) [7](#source-7)

[FACT] Cleanup runs at startup, every ten minutes and after every admitted
mutation finishes setup. Bursts coalesce. An idle worker has five minutes,
takes the native space lock and snapshots catalogs. Busy, draining or uncertain
controllers skip maintenance. Admitted mutations cancel it; failed receipt
admission does not. Reads bypass it. Cancellation is quiet; deadline failures
are logged. The worker's process group is terminated on cancellation.
[5](#source-5) [6](#source-6) [14](#source-14)

[FACT] Inventory retains all declared bases/resolved images and inspects every
container, including stopped and unlabelled ones. Empty or invalid declared
images block persistence/resolution with a box-attributed error; loading remains
possible for repair. Candidates need both the composed repository namespace and
`yoloctl.kind=composed`. Tagged-image inspections use batches of 64, validating
response count, identity and reference association before any deletion.
Normalized names, aliases and immutable identities retain candidates.
[7](#source-7) [8](#source-8) [15](#source-15) [25](#source-25) [29](#source-29)

[FACT] nerdctl 2.3.5 reports a mutable image name: retention uses the actual
overlayfs snapshot parent's OCI ChainID, potentially retaining extra images with
identical layers. nerdctl 2.4 can report a recorded manifest/index digest;
containers without that label still need snapshot lookup. Manifest and platform
config digests differ; retention matches both. Incomplete inventories abort,
including unnamed containers (with their ID in the error). Untagged image rows
are skipped. `image prune` shares these rules. [7](#source-7) [8](#source-8)
[15](#source-15) [21](#source-21) [22](#source-22) [26](#source-26) [31](#source-31)

[FACT] Successful Firecracker background passes run bounded, cancellable `fstrim`
on owned system/native ext4 mounts, never shared host paths. Unsupported trim
is diagnostic; it does not block foreground builds. [5](#source-5)

[FACT] Cleanup removes only obsolete composed tags and unused BuildKit cache.
No boxes, volumes, workdirs, account data or declarations are pruned. Before
builds/pulls, root free space below 20% triggers a bounded 30-second cleanup
retaining that build's input/output references. Its timeout reports shortage
and incomplete cleanup, separately from user cancellation. Pressure may discard
all unused cache. Free bytes are remeasured for up to three seconds afterward;
persistent shortage refuses the build. Shared-layer totals are not physical
recovery measurements. [7](#source-7) [9](#source-9) [16](#source-16)

[FACT] Tests cover retention, partial inventories, both nerdctl identity paths,
invalid-image repair, cancellation/deadlines, wakeups, delayed reclamation and
rollback ordering/markers. Virtual time exercises bounded waits without real
delays. The isolated VM smoke waits for the post-cache-prune journal/Firecracker VM-log summary and
checks retained images/container state/workdir data. Unit coverage does not
establish runtime validation of nerdctl 2.4's digest path.
[10](#source-10) [11](#source-11) [17](#source-17) [18](#source-18)
[19](#source-19) [20](#source-20) [27](#source-27) [30](#source-30)

[FACT] Controller image preparation retains private input/output references and
containerd leases through consumer apply. Low-disk cleanup uses its own catalog
snapshot; startup recovery sweeps owned abandoned private names/leases while
retaining legacy uncertain effects. Abandoned work may survive best-effort
cancellation; the orphan ledger is separate from receipt retention. See
[image preparation](image-preparation.md) for the verified boundary.

## Sources

1. <a id="source-1"></a> [buildkit.go](../../internal/storagegc/buildkit.go#L16) — policy.
2. <a id="source-2"></a> [BuildKit 0.17 GCPolicy](https://github.com/moby/buildkit/blob/v0.17.0/cmd/buildkitd/config/config.go#L160-L184) — targets.
3. <a id="source-3"></a> [buildkit_gc.go](../../cmd/yoloctl/buildkit_gc.go#L17) — activation/rollback.
4. <a id="source-4"></a> [controller_internal.go](../../cmd/yoloctl/controller_internal.go#L108) — install-service.
5. <a id="source-5"></a> [controller_storage.go](../../cmd/yoloctl/controller_storage.go#L27) — scheduler/worker.
6. <a id="source-6"></a> [maintenance.go](../../internal/controller/maintenance.go#L9) — admission.
7. <a id="source-7"></a> [storage_gc.go](../../cmd/yoloctl/storage_gc.go#L30) — inventory/preflight.
8. <a id="source-8"></a> [policy.go](../../internal/storagegc/policy.go#L46) — retention.
9. <a id="source-9"></a> [container.go](../../internal/container/container.go#L293) — build/pull guards.
10. <a id="source-10"></a> [policy_test.go](../../internal/storagegc/policy_test.go#L11) — policy tests.
11. <a id="source-11"></a> [storage-gc.sh](../../scripts/smoke/storage-gc.sh#L1) — isolated smoke.
12. <a id="source-12"></a> [policy.go](../../internal/storagegc/policy.go#L19) — measured disk budgets.
13. <a id="source-13"></a> [controller_deploy.go](../../cmd/yoloctl/controller_deploy.go#L151) — deployment.
14. <a id="source-14"></a> [server.go](../../internal/controller/server.go#L60) — drain/mutations.
15. <a id="source-15"></a> [storage.go](../../internal/container/storage.go#L27) — snapshot/batch inspection.
16. <a id="source-16"></a> [controller_internal.go](../../cmd/yoloctl/controller_internal.go#L413) — build guard.
17. <a id="source-17"></a> [image_prune_test.go](../../cmd/yoloctl/image_prune_test.go#L93) — complete inventories.
18. <a id="source-18"></a> [storage_gc_test.go](../../cmd/yoloctl/storage_gc_test.go#L16) — pressure/remeasurement.
19. <a id="source-19"></a> [buildkit_gc_test.go](../../cmd/yoloctl/buildkit_gc_test.go#L15) — rollback/markers.
20. <a id="source-20"></a> [maintenance tests](../../internal/controller/maintenance_test.go#L10) — admission/cancellation.
21. <a id="source-21"></a> [OCI 1.1.1 ChainID](https://github.com/opencontainers/image-spec/blob/v1.1.1/identity/chainid.go#L23-L61).
22. <a id="source-22"></a> [containerd 2.1.4 withNewSnapshot](https://github.com/containerd/containerd/blob/v2.1.4/client/container_opts.go#L237-L267).
23. <a id="source-23"></a> [BuildKit 0.31.2 pruning](https://github.com/moby/buildkit/blob/v0.31.2/cache/manager.go#L1042-L1162) — shared/active cache.
24. <a id="source-24"></a> [Lima 2.0 boot](https://github.com/lima-vm/lima/blob/v2.0.0/pkg/cidata/cidata.TEMPLATE.d/boot/40-install-containerd.sh#L60-L73) — preserves config.
25. <a id="source-25"></a> [ValidateImage](../../internal/agentbox/resolved_image.go#L12) — declared references.
26. <a id="source-26"></a> [nerdctl 2.3.5 inspect](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/inspecttypes/dockercompat/dockercompat.go#L344-L351) — mutable name.
27. <a id="source-27"></a> [controller_storage_test.go](../../cmd/yoloctl/controller_storage_test.go#L16) — scheduler/process tests.
28. <a id="source-28"></a> [Lima 1.2 boot](https://github.com/lima-vm/lima/blob/v1.2.0/pkg/cidata/cidata.TEMPLATE.d/boot/40-install-containerd.sh#L44-L64) — overwrites config.
29. <a id="source-29"></a> [Save](../../internal/agentbox/agentbox.go#L557) — persistence validation.
30. <a id="source-30"></a> [agentbox_reconcile_test.go](../../cmd/yoloctl/agentbox_reconcile_test.go#L119) — invalid-image repair.
31. <a id="source-31"></a> [nerdctl 2.4 inspect](https://github.com/containerd/nerdctl/blob/v2.4.0/pkg/inspecttypes/dockercompat/dockercompat.go#L344-L369) — recorded digest.
32. <a id="source-32"></a> [Guest builder](../../internal/vmguest/buildkit_linux.go#L1) — `configureBuilder`, shared policy before lazy startup.
