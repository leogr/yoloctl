# Data disk size and offline grow

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; rotated-log search at `4fa6761c87967cc6191ec19130898e60f0a92720` and accompanying changes.

[FACT] `space create --data-disk SIZE` sets the data disk (accounts and
declarations): whole GiB, at least 4 GiB, default 10 GiB; spaces saved before
the setting are 10 GiB. It is never autosized. The adapter allocates it at the
saved size (Firecracker a sparse `data.ext4`, Lima `limactl disk create
--size`). [1](#source-1) [2](#source-2)

[FACT] `space set --data-disk SIZE` (or `space edit`'s `data_disk:`) on a
verified stopped space only grows it. It refuses on Lima (Lima mounts the disk
before yoloctl runs, so there is no offline step), for an uninitialized disk,
while another grow is pending, and when the VM's image predates the grow
(`run yoloctl space recreate NAME --update-vm-image first`). Before any
change the host reads `data.ext4`'s superblock natively under the instance
lock of the stopped VM and requires the space's filesystem UUID at the saved
size. It then saves a `requested` checkpoint (and the new size), makes a
reflink copy `data.ext4.pre-grow` unless `--no-backup`, extends the file
sparsely and saves `extended`. A host filesystem without reflink refuses,
restoring the bootstrap, with `space export` advised before `--no-backup`;
a repeated set resumes an interrupted `requested`. [2](#source-2)
[3](#source-3) [4](#source-4)

[FACT] At the next start the helper passes an `extended` grow in the boot
record. Guest init, before mounting `/dev/vdb`, requires the UUID, a
`To`-byte device and a `From` (or already `To`) filesystem, runs
`e2fsck -f -p` (exit 0 or 1), `resize2fs`, then a clean `e2fsck -f -n`,
re-reads the superblock and prints a verified line to the console. Any other
outcome leaves the disk unmounted and fails the boot with the reason, keeping
`extended`, so the next start retries from the check. [5](#source-5)
[6](#source-6)

[FACT] The host records completion only with the VM stopped: `space stop` and
`space set`/`edit` check that the superblock shows the new size and UUID and
that the VM log holds init's verified line within its last 4 MiB (across
current and previous generations), then drop the checkpoint and the
copy and print `data disk 10 GiB → 20 GiB, verified`. `space show` reports a
pending phase or that a grow needs an image update; `--json` adds
`data_disk_bytes` and `data_disk_grow`. Export refuses while a grow is
pending. [2](#source-2) [7](#source-7)

[FACT] Never reformat: formatting keeps requiring the pending-initialization
authority (`Initializing` and no UUID) and also no grow, while a grow
checkpoint validates only for an initialized disk with a UUID. Images record
the init's grow (`data_disk_grow: 1` in `image.json`); assembly requires
`e2fsck` and `resize2fs` in the guest root and the recipe version is 3.
[1](#source-1) [6](#source-6) [8](#source-8)

[FACT] On a probe disk formatted like these (go-diskfs, no `resize_inode`),
offline `resize2fs` 1.47.0 grew 1 → 2 → 10 GiB e2fsck-clean, kept the UUID
and file contents, in 0.03 s. Regressions repeat that with real e2fsprogs
(skipped without them) and cover every refusal, the checkpoint order, reflink
fallback, stopped-only reads and extends, the boot record and the log check.
`make smoke-disk` (Firecracker, disposable space) runs the whole path live.
[9](#source-9)

## Sources

1. <a id="source-1"></a> [Sizes and checkpoint](../../internal/spaceconfig/disk.go#L50-L106) — `DataDiskBytes`, `DataDiskGrow`, `validate`; `Bootstrap.DataDiskGrow` and `VMSettings.DataDisk` in bootstrap.go.
2. <a id="source-2"></a> [CLI](../../cmd/yoloctl/space_disk.go#L131-L280) — `selectSpaceDataDisk`, `growSpaceDataDisk`, `settleDataDiskGrow`, `dataDiskShown`; `changeSpaceResources` in space_resources.go; the stop case in controller_lifecycle.go; the export guard in space_archive.go.
3. <a id="source-3"></a> [Host grow](../../internal/firecracker/datadisk_linux.go#L1-L235) — `stopped`, `DataDiskGrowSupported`, `DataDiskState`, `ExtendDataDisk`, `cloneDataDisk`, `DataDiskGrowLogged`, `RemoveDataDiskBackup`, `pendingDataDiskGrow`.
4. <a id="source-4"></a> [Superblock](../../internal/ext4sb/ext4sb.go#L1-L46) — `Read`; adapter interface `DataDiskGrower` and `ErrNoReflink` in internal/vmapi/storage.go; `CreateAccountDisk` sizes in internal/vm/vm.go.
5. <a id="source-5"></a> [Guest grow](../../internal/vmguest/datadisk_linux.go#L1-L87) — `growDataDisk`; called before the data disk mount in init_linux.go `boot`.
6. <a id="source-6"></a> [Boot record](../../internal/vmwire/boot.go#L18-L43) — `Boot.DataDiskGrow`, `GrownLine`; `bootImage` and the format guard in internal/firecracker/helper_linux.go.
7. <a id="source-7"></a> [Views](../../cmd/yoloctl/controller_views.go#L152-L215) — `controllerSpaceShow` data disk fields; `writeSpaceShow`/`writeSpaceShowJSON` in space.go.
8. <a id="source-8"></a> [Image](../../internal/firecracker/config.go#L28-L43) — `Image.DataDiskGrow`, `DataDiskGrowVersion`; `verifyRuntime` and the manifest in assemble_linux.go; `imageRecipeVersion` in recipe_linux.go.
9. <a id="source-9"></a> [Regressions](../../cmd/yoloctl/space_disk_test.go#L145-L306) — CLI grow, refusals, views and export; internal/vmguest/datadisk_linux_test.go, internal/firecracker/datadisk_linux_test.go and superblock_test.go, internal/spaceconfig/disk_test.go; scripts/smoke/disk.sh.
