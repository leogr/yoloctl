# Firecracker host shares

Verification: `8931e8df3abebc5c0fdd6f77bd473110a2e7d23b` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Each host grant has its own `os.Root` and SFTP server. The guest mounts
SSHFS 3.7.3 with attribute/content caching disabled and normal guest permission
checks. Read-only grants reject mutations on both pathname and handle APIs.
Absolute/escaping symlinks are unsupported. These shares carry projects and
read-only catalogs; native accounts/declarations remain on ext4. Open descriptors
retain their original objects across host renames, as with `os.Root`.
[1](#source-1) [2](#source-2) [3](#source-3) [13](#source-13)

[FACT] The server pins `pkg/sftp/v2 v2.0.0-alpha2` and two reproducible patches:
bounded counted-field decoding from upstream PR 649, and connection/open-handle
cleanup on every `Serve` exit, including failed negotiation. The local replacement
is checksum-verified and licensed with its upstream sources. V1 remains an
independent test client, not the production server. This is an alpha dependency;
the patches do not establish correctness of its unused APIs such as
`GracefulStop`. [4](#source-4) [5](#source-5) [6](#source-6) [14](#source-14)

[FACT] Open-file metadata, truncation, timestamps and fsync operate on the actual
file descriptor, including after rename/unlink. `fsync@openssh.com` calls
`File.Sync`; synchronous per-connection request dispatch keeps it behind prior
writes. OPEN/MKDIR apply requested permission bits at creation, subject to host
umask; reopening an existing file does not chmod it. Path-based metadata uses
rooted operations without requiring read permission. Foreign ownership changes,
extended attributes and non-regular file opens are rejected. [1](#source-1)
[7](#source-7) [8](#source-8)

[FACT] SSHFS provides file fsync but no fsyncdir callback. The server therefore
syncs affected parent directories before acknowledging successful create,
mkdir, unlink, rmdir, rename, hardlink or symlink operations. Mutation and sync
use the same pinned parent descriptors; rename/link sync both parents. A sync
failure reports an error after the namespace has changed; it does not roll back
the mutation. This does not flush file data that the application has not fsynced,
provide cross-host/guest locks, or make SSHFS a substitute for native database
storage. [3](#source-3) [9](#source-9) [10](#source-10)

[FACT] Go tests use raw packets and the v1 client to cover creation modes,
renamed/unlinked handles, mode-000 metadata, read-only/escaping paths, special
files, malformed counts/packets, disconnect cleanup, fsync ordering/failure,
parent-sync ordering/failure, large transfer bytes and concurrent connections.
Mode-preservation fixtures explicitly establish their initial permissions rather
than assuming creation bypasses umask. Read handles on writable grants exercise
descriptor chmod after rename and unlink; read-only grants still reject it.
A macOS host script runs the package and race tests under masks 022 and 077,
recording the actual Go/host architectures, without creating a VM.
The lifecycle smoke separately exercises the mounted filesystem and checks
bytes from the host. Unit and local-filesystem passes are not SSHFS/KVM runtime
validation or power-loss durability proof. [6](#source-6) [7](#source-7)
[8](#source-8) [10](#source-10) [11](#source-11) [12](#source-12) [15](#source-15) [16](#source-16)

## Sources

1. <a id="source-1"></a> [Share adapter](../../internal/vmshare/share.go#L24) — `Share`, `Open`, `fileHandle.SetStat`, `SetStat`.
2. <a id="source-2"></a> [Guest mount](../../internal/vmguest/init_linux.go#L375) — `share`.
3. <a id="source-3"></a> [Namespace operations](../../internal/vmshare/namespace.go#L12) — pinned parents, exclusive creation, synchronization.
4. <a id="source-4"></a> [Vendor recipe](../../scripts/vendor-sftp.py#L11) — alpha pin/checksum, patches, license and regeneration.
5. <a id="source-5"></a> [Patched server](../../third_party/sftp-v2/server.go#L386) — `Serve` dispatch/cleanup.
6. <a id="source-6"></a> [Server regressions](../../internal/vmshare/server_test.go#L62) — cleanup, count limits, fsync order/errors, modes and listing.
7. <a id="source-7"></a> [Handle regressions](../../internal/vmshare/handle_test.go#L10) — rename/unlink metadata and mode-000 path access.
8. <a id="source-8"></a> [Protocol regressions](../../internal/vmshare/protocol_test.go#L32) — creation attributes, FSETSTAT and fsync.
9. <a id="source-9"></a> [SSHFS 3.7.3](https://github.com/libfuse/sshfs/blob/sshfs-3.7.3/sshfs.c) — `sshfs_fsync`, `sshfs_oper`, no fsyncdir callback.
10. <a id="source-10"></a> [Namespace regressions](../../internal/vmshare/namespace_test.go#L16) — affected parents, host rename, failure, dangling symlinks.
11. <a id="source-11"></a> [Validation regressions](../../internal/vmshare/validation_test.go#L22) — bytes, times, concurrent connections, malformed packets and read-only modes.
12. <a id="source-12"></a> [Mounted-filesystem smoke](../../scripts/smoke/vm-shares.py#L10) — guest operations.
13. <a id="source-13"></a> [Guest runtime pins](../../assets/vm/firecracker/runtime-pins.json#L14) — SSHFS 3.7.3 package.
14. <a id="source-14"></a> [Count-bounds changes](https://github.com/pkg/sftp/pull/649/commits/a422f1388530aaa616ebe3de224d9f6bb6a313cb) — upstream patch source.
15. <a id="source-15"></a> [Host verification](../../scripts/smoke/space-lifecycle.sh#L74) — guest probe plus host bytes/modes.
16. <a id="source-16"></a> [macOS unit/race verification](../../scripts/check-vmshare-macos.sh#L1) — umask matrix and recorded toolchain/architecture; no mounted-filesystem qualification.
