# VM system disk size

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] A new space's system disk is `--disk SIZE` or, by default, `auto`:
half of the bytes available on the host filesystem that will hold the VM
(the directory above its instance directory, Firecracker's instances
directory or Lima's home, or its nearest existing ancestor), rounded down to
whole GiB and clamped to 30–50 GiB. It prints `system disk 40 GiB (half of
80 GiB free)` and saves the size in the bootstrap; nothing measures it again.
An explicit size is whole GiB of at least 20 GiB with no cap. A saved size
below 20 GiB or not whole GiB fails bootstrap validation; existing spaces keep
their saved 30 GiB. Import keeps the archive's size. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] The floor applies on a small host: when the size exceeds the free
space, create prints that the VM cannot fill its disk (and, on Firecracker,
that the host pauses it at 2 GiB free). Firecracker refuses before anything is
written when the free space is below the 4 GiB start headroom plus the format
allocation, naming both amounts. [2](#source-2) [4](#source-4)

[FACT] Firecracker formats system disks with one inode per 16 KiB at every
size, so formatting allocates the zeroed inode tables at once: measured with
the same go-diskfs parameters on an overlay filesystem, 30 GiB took 1.4 s and
610 MiB, 60 GiB 5.1 s and 1.09 GiB, 100 GiB 15.5 s and 1.7 GiB (real ext4 or
xfs hosts differ in time). `FormatAllocation` estimates it as 256 B per inode,
2% for descriptors and bitmaps and a 128 MiB journal, within 2% of those
measurements; VM creation requires 4 GiB plus that amount, so a format cannot
by itself bring the host to the 2 GiB pause. Create and recreate print
`formatting the N GiB system disk…`. Lima allocates nothing for it on the
host. [4](#source-4) [5](#source-5)

[FACT] `space set --disk SIZE` and `space edit` (`disk:`) on a verified
stopped space only grow the saved size; a smaller or malformed value is
refused and nothing changes. The size is saved and applies at the next
`space recreate`, which replaces the system disk anyway; no adapter call
resizes a disk, and start never does. `space show` and `space list` name a
saved size the VM does not have yet (`disk 50GiB saved, applies at the next
space recreate`), `space show --json` adds `saved_disk_bytes`, and recreate
prints `system disk 30 → 50 GiB (the saved setting)`. [2](#source-2)
[3](#source-3) [6](#source-6)

[FACT] Regressions cover the sizing table (80→40, 120→50, 50→30, 20→30 with
the notice, 4.2 GiB refused, 200→50, explicit sizes and floors), the format
estimate against the measurements, the metadata-aware create headroom,
grow-only set with its refusals and notice, and the show/list wording.
[7](#source-7)

## Sources

1. <a id="source-1"></a> [Sizes](../../internal/spaceconfig/disk.go#L1-L48) — `VMDiskBytes`, `AutoVMDisk`, `DiskSize`; `Validate` and `Split` in bootstrap.go; import copies `m.Bootstrap.VM` in cmd/yoloctl/space_archive.go.
2. <a id="source-2"></a> [CLI](../../cmd/yoloctl/space_disk.go#L1-L129) — `selectSpaceDisk`, `resolveSpaceDisk`, `diskCapacityNotice`, `growSpaceDisk`, `pendingDiskNote`, `diskShown`; `createControllerSpace` in controller_lifecycle.go.
3. <a id="source-3"></a> [Set and edit](../../cmd/yoloctl/space_resources.go#L32-L248) — `spaceResourceDocument.Disk`, `changeSpaceResources`, `editSpaceResources`.
4. <a id="source-4"></a> [Format estimate](../../internal/vmapi/storage.go#L9-L31) — `HostStorageStartFree`, `FormatInodeRatio`, `FormatAllocation`; `requireHostStorageFor` in internal/firecracker/host_storage_linux.go, used by `Driver.Create`.
5. <a id="source-5"></a> [Measured cost](../../cmd/yoloctl/space_disk_test.go#L57-L67) — `TestFormatAllocationMatchesTheMeasuredCost` holds the 2026-10-28 measurement (go-diskfs `ext4.Create`, `SectorsPerBlock: 8, InodeRatio: 16384`, sparse file); the format notice precedes `CreateWithSpec` in cmd/yoloctl/controller_deploy.go.
6. <a id="source-6"></a> [Views and recreate](../../cmd/yoloctl/space.go#L1125-L1588) — `spaceShowData.SavedDisk`, `writeSpaceShow`, `writeSpaceShowJSON`, `printInstances`; `controllerSpaceShow`/`controllerSpaceList` in controller_views.go; the recreate line in controller_lifecycle.go.
7. <a id="source-7"></a> [Regressions](../../cmd/yoloctl/space_disk_test.go#L1-L139) — CLI sizing, set and views; internal/spaceconfig/disk_test.go; `TestCreateHeadroomCountsTheFormatAllocation` in internal/firecracker/host_storage_linux_test.go.
