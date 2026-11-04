# VM disks in space view and `space storage`

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; disk size range `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Each telemetry sample reads `statfs` of the data disk mount (`disk`) and
of the VM root `/` (`system_disk`): total and `Bavail` bytes. A failed read
leaves the field absent, never an empty disk. On Firecracker `/` is an overlay
whose upper directory, `/var/lib/containerd` and `/var/lib/buildkit` all live
on the system ext4 disk; on Lima `/` is that disk. [1](#source-1) [2](#source-2)
[3](#source-3)

[FACT] One shared rule classifies both disks: warn below min(4 GiB, 15% of
total) free or from 85% used, critical below min(2 GiB, 10%) free or from 95%
used. A 30–50 GiB system disk keeps the 4/2 GiB floors; the 10 GiB data disk
warns at 85% used. Used is total minus `Bavail`, so it includes the filesystem
reserve. [4](#source-4)

[FACT] Space view's `disks` band shows the system disk ("boxes, images, build
caches") and the data disk ("accounts & box settings (kept across VM
rebuilds)") with gauge, used/total and free GiB, side by side from 90 inner
columns and stacked below; account chips follow. A warning or critical disk
turns its gauge fill and free-space text amber or red. Unknown usage reads
`usage unavailable`. Each disk is a hover/click target: hover describes it with
its numbers, click opens its popup. [5](#source-5) [6](#source-6)

[FACT] The title row's warning slot, after the authority warnings, names each
disk that needs attention (`⚠ system disk 88%`, `✖ system disk almost full`);
a tight row keeps only the worst disk as a bare percentage. A disk toasts once
when it reaches a worse level than it has toasted since it was last fine;
recovery is quiet. The compact layout below 48 columns has no band and relies
on the title. [7](#source-7) [8](#source-8)

[FACT] `space.storage` is a registered non-mutating, host-only read (box
operators are refused). Host `space storage [SPACE] [--json]` runs the usual
bundle/running-VM check and asks the controller, which measures within 20 s
without a lock. It reports both disks, every container's writable layer
(stopped and non-box containers included), images, BuildKit and the
remainder. [9](#source-9) [10](#source-10) [11](#source-11)

[FACT] The system disk is partitioned once per overlayfs snapshot in
namespace `default` (`ctr snapshots usage -b`): a container's snapshot key
(full ID from native inspect, matched to the 12-character ID `nerdctl ps`
prints by unique prefix [22](#source-22)) is its box; `sha256:` ChainID names are image layers;
any other snapshot is BuildKit cache, since its containerd worker uses the
same namespace. Image blobs (`io.containerd.content.v1.content`) and
`/var/lib/buildkit` are walked without following symlinks or crossing
filesystems, each inode once. The remainder is "guest OS and other". A box's
home, `/tmp` and `/var/tmp` are in its writable layer; an ephemeral workdir is
a VM-local bind, so it falls in the remainder; host workdirs are not on this
disk. [21](#source-21) A part
that cannot be measured is null with its reason; the remainder is then null,
and an unkeyed container also withholds BuildKit. [11](#source-11)
[12](#source-12) [13](#source-13)

[FACT] containerd v2.3.3's overlay snapshotter walks an active snapshot's
upper directory for `Usage` and returns a committed snapshot's stored size;
`ctr snapshots usage` without keys walks every snapshot, and `-b` prints
bytes. One snapshot removed mid-walk fails that call. [14](#source-14)
[15](#source-15)

[FACT] The system disk popup shows live usage, what the disk holds and how to
free it, then runs `space storage SPACE --json` on demand: rows largest first,
unavailable parts and per-item reasons. [5](#source-5) [16](#source-16)

[FACT] Tests cover the classifier, sample fields, parsers, partition sum,
unavailable parts, operator refusal, widths, thresholds, toasts, hover and
popups with fixtures. No live VM measurement is established by them.
[17](#source-17) [18](#source-18) [19](#source-19) [20](#source-20)

## Sources

1. <a id="source-1"></a> [collect.go](../../internal/spacetelemetry/collect.go#L89-L98) — `Sample` disk reads; `SystemDisk` in `telemetry.go` at L151-L154.
2. <a id="source-2"></a> [space_telemetry.go](../../cmd/yoloctl/space_telemetry.go#L93-L100) — native collector with `SystemMount: "/"`.
3. <a id="source-3"></a> [init_linux.go](../../internal/vmguest/init_linux.go#L268-L292) — runtime directories bound from `/run/system`; root overlay at L638-L646.
4. <a id="source-4"></a> [disk.go](../../internal/spacetelemetry/disk.go#L1-L56) — `DiskLevel`, thresholds, `Used`, `WarnFree`, `CriticalFree`, `Pressure`.
5. <a id="source-5"></a> [disks.go](../../internal/tui/spaceview/disks.go#L14-L166) — `disks`, `levelGauge`, `diskBlock`, `diskBand`; popups at L234-L278.
6. <a id="source-6"></a> [layout.go](../../internal/tui/spaceview/layout.go#L411-L421) — disk hover hints; `tSystemDisk` at L29; click in `popup.go` at L164.
7. <a id="source-7"></a> [disks.go](../../internal/tui/spaceview/disks.go#L168-L230) — `diskAlerts`, `diskEvents`; title slot in `layout.go` at L255-L270.
8. <a id="source-8"></a> [data.go](../../internal/tui/spaceview/data.go#L168) — disk events per sample.
9. <a id="source-9"></a> [controller_internal.go](../../cmd/yoloctl/controller_internal.go#L105) — `space.storage` entry; view in `controller_views.go` at L256.
10. <a id="source-10"></a> [space_storage.go](../../cmd/yoloctl/space_storage.go#L254-L310) — `newSpaceStorageCmd`; registered in `controller_lifecycle.go` at L45.
11. <a id="source-11"></a> [space_storage.go](../../cmd/yoloctl/space_storage.go#L22-L252) — report types, `collectSpaceStorage`, `runSpaceStorageView`.
12. <a id="source-12"></a> [storage.go](../../internal/container/storage.go#L129-L220) — `ContainerSnapshots`, `SnapshotFor`, `SnapshotUsages` and parsers.
13. <a id="source-13"></a> [storage_usage_linux.go](../../cmd/yoloctl/storage_usage_linux.go#L15-L70) — `diskUsage`.
14. <a id="source-14"></a> [containerd v2.3.3 ctr snapshots](https://github.com/containerd/containerd/blob/v2.3.3/cmd/ctr/commands/snapshots/snapshots.go) — `usageCommand`.
15. <a id="source-15"></a> [containerd v2.3.3 overlay snapshotter](https://github.com/containerd/containerd/blob/v2.3.3/plugins/snapshots/overlay/overlay.go) — `Usage`.
16. <a id="source-16"></a> [disks.go](../../internal/tui/spaceview/disks.go#L280-L376) — `storageReport`, `storageLines`.
17. <a id="source-17"></a> [telemetry_test.go](../../internal/spacetelemetry/telemetry_test.go#L421) — `TestDiskPressure`, `TestSystemDiskUnavailable`.
18. <a id="source-18"></a> [storage_test.go](../../internal/container/storage_test.go#L95-L135) — snapshot parser tests.
19. <a id="source-19"></a> [space_storage_test.go](../../cmd/yoloctl/space_storage_test.go#L1) — partition, unavailable parts, operator refusal, `diskUsage`.
20. <a id="source-20"></a> [disks_test.go](../../internal/tui/spaceview/disks_test.go#L1) — band, thresholds, toasts, hover and popups.
21. <a id="source-21"></a> [workdir.go](../../internal/agentbox/workdir.go#L131-L135) — `NewEphemeralWorkdir`; `AgentboxDirHost` in `internal/sandbox/sandbox.go` at L84-L86.
22. <a id="source-22"></a> [nerdctl v2.3.5 container list](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/cmd/container/list.go) — `ListItem.ID` truncated to 12 characters unless `--no-trunc`.
