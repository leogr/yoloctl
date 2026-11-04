# Native data disk and integrity

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Native account state and controller configuration share a dedicated
space-owned native ext4 data disk. Layout
helpers derive the disk mount and `/var/lib/yoloctl/account-state/<account>/<agent>`
leaf. The guest helper verifies filesystem type, recorded UUID, ownership marker,
and the live root bind's device/inode identity. Account access does not fall
back to an empty directory when those checks fail. Controller workers and
recovery use read-only verification; explicit initialization/deployment/restoration
owns preparation of a missing native bind. [14](#source-14) [1](#source-1) [2](#source-2)

[FACT] `ExecuteForDisk` validates requests and sends JSON on stdin to the guest
Python helper (locally for native workers); file contents are not passed in argv. Guest operations use directory
file descriptors and no-follow opens. Writes create an exclusive temporary
0600 file, set its owner, fsync, rename and fsync the directory. Native removal
uses symlink-resistant directory-relative traversal. [2](#source-2) [3](#source-3)

[FACT] New space creation durably publishes its boot identity and initialization
seed before allocating the disk. Interrupted allocation can create only that
pending, absent disk; initialized disks never get empty replacements. Preparation records the
observed filesystem UUID only after revoking adapter formatting permission. Controller
reuse verifies the live native root and creates individual leaves lazily for
stateful agents; it does not rewrite Lima YAML or mkdir every account on each
running-space check. [4](#source-4) [5](#source-5)

[FACT] An interrupted first boot has a narrowly guarded formatting retry:
initialization must still be pending, the recorded UUID empty, the VM stopped
or absent, the disk detached, and every guest-visible byte zero. Missing UUID
or missing filesystem signature alone is insufficient. The Lima adapter inspects raw and standalone
QCOW2 images read-only; QCOW2 backing files, snapshots, encryption,
dirty/incompatible metadata and invalid table bounds are rejected. The scan
checks cancellation and file size/mtime stability. Firecracker uses raw sparse
files and checks all allocated extents under its disk lock before formatting.
[5](#source-5) [6](#source-6) [13](#source-13)

[FACT] Lima disk YAML updates preserve other additional disks and comments,
reject duplicate owned entries, and leave already-correct files byte-identical.
Recovery tests cover blank/nonblank and unsafe images. The synthetic QCOW2
fixture exercises decoding without external QEMU; optional tests use real
QEMU tools when installed. [7](#source-7) [8](#source-8)

[FACT] Storage regressions distinguish live attachment from persisted recovery.
The smoke suite runs append/lock/shared-mapping/SQLite checks through the native
bind, then preserves large synthetic JSONL records across fresh processes, box
replacement and VM replacement. Expected bytes are generated from record count,
not read from a second file on the storage being tested. The local oracle tests
reject zero-filled ranges, truncation, missing turns and duplicates before
resume can write. [9](#source-9) [10](#source-10) [12](#source-12)

[FACT] These are synthetic tests. They do not log in to a provider, test actual
provider conversation indexes, simulate power loss, or establish host-workdir
share correctness. The workdir path is still a distinct host-share path; the
account disk's preservation contract does not cover it. [9](#source-9)
[10](#source-10) [11](#source-11)

## Sources

1. <a id="source-1"></a> [internal/layout/layout.go](../../internal/layout/layout.go#L267-L315) — `AccountStateRoot` through `AccountDiskDir`.
2. <a id="source-2"></a> [internal/accountstate/guest.py](../../internal/accountstate/guest.py#L1-L240) — `directory`, `regular`, `write`, `run`.
3. <a id="source-3"></a> [internal/accountstate/state.go](../../internal/accountstate/state.go#L77) — ExecuteForDisk, verified disk-scoped native operations.
4. <a id="source-4"></a> [cmd/yoloctl/controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L153) — pending identity publication before allocation.
5. <a id="source-5"></a> [cmd/yoloctl/controller_deploy.go](../../cmd/yoloctl/controller_deploy.go#L115) — initialization and preserved disk verification.
6. <a id="source-6"></a> [internal/lima/disk_blank.go](../../internal/lima/disk_blank.go#L23-L140) — `AccountDiskBlank` through `openAccountQCOW2`.
7. <a id="source-7"></a> [internal/lima/disk_yaml.go](../../internal/lima/disk_yaml.go#L16-L86) — `setInstanceAccountDisk`.
8. <a id="source-8"></a> [internal/lima/disk_qcow2_test.go](../../internal/lima/disk_qcow2_test.go#L1-L155) — `TestAccountDiskBlankQCOW2`, `TestAccountDiskBlankWithQEMUTools`.
9. <a id="source-9"></a> [scripts/smoke/agent-state.sh](../../scripts/smoke/agent-state.sh#L1-L154) — `history_probe`, native storage and replacement phases.
10. <a id="source-10"></a> [scripts/smoke/native-large-append.py](../../scripts/smoke/native-large-append.py#L1-L86) — `record`, `verify`, `append_batch`, `main`.
11. <a id="source-11"></a> [internal/agentbox/workdir.go](../../internal/agentbox/workdir.go#L1-L160) — `Workdir`, `CanonicalizeWorkdir`, `ContainerPath`.
12. <a id="source-12"></a> [internal/accountstate/history_test.go](../../internal/accountstate/history_test.go#L20-L73) — `TestSyntheticHistoryResumesAcrossProcesses` through `TestSyntheticHistoryRejectsCorruptionBeforeResume`.

13. <a id="source-13"></a> [Firecracker disks](../../internal/firecracker/disk_linux.go#L1) — `AccountDiskBlank`, `blank`, `formatDisk`.

14. <a id="source-14"></a> [internal/controller/runtime.go](../../internal/controller/runtime.go#L19) — `LoadRuntime`, `VerifyRuntimeStorage`, `PrepareRuntimeForLifecycle`; deployController and restoreNativeSpace select preparation explicitly.
