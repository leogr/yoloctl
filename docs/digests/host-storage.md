# Firecracker host storage pressure

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; system-disk format headroom `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Firecracker's writable system and account disks are sparse host files.
Guest free blocks do not measure the host's ability to allocate their next
write. Account-disk creation, system-disk creation and VM startup require at
least 4 GiB available on each relevant backing filesystem; system-disk
creation adds what formatting that disk allocates
([VM system disk size](vm-disk-size.md)). This is a headroom
check, not preallocation, a quota or a promise that a build will fit.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The existing per-VM helper pins both writable disk descriptors before
boot. It measures `fstatfs().Bavail * Bsize`, excluding host filesystem reserves;
it does not infer capacity from the project share or guest root. Guest-side
fullness is shown separately by space view (see [VM disks](space-view-disks.md)). Final-component
symlinks and special files are rejected. The two disks may reside on different
filesystems; either can block startup or recovery. [1](#source-1) [4](#source-4)

[FACT] While the VM runs, the helper checks once per second. Below 2 GiB, or if
measurement fails, it sends `SIGSTOP` to the identity-verified VMM through a
pidfd. The helper remains alive. Once both backing filesystems report at least
4 GiB, it sends `SIGCONT`. It neither restarts workloads nor replays requests.
The larger recovery threshold avoids oscillation at the pause boundary.
Signal failures are logged and retried; they do not claim successful suspension.
[1](#source-1) [5](#source-5)

[FACT] A private, bounded, read-only Unix socket publishes the helper's in-memory
pause reason. It accepts no commands and is not exposed to guests or boxes.
The status does not depend on writing a new file during a shortage. Host
`space list/show` report `Paused` and `status_detail`; new SSH/controller requests
report the host-storage reason. Stop/start refuse a known pause, and the
adapter refuses even forced shutdown while it observes that pause, preserving
the live VM for recovery. Free host space and wait for automatic continuation
before ordinary lifecycle work. [1](#source-1) [3](#source-3) [6](#source-6)
[7](#source-7)

[FACT] The guard changes only the Linux amd64 Firecracker host adapter. It
requires neither a new guest protocol/image nor another host daemon. An already
running older helper has no status socket; updating the CLI/controller alone
does not retrofit monitoring. A normal space stop/start with the new CLI starts
the guard. Saved declarations/accounts survive; agent sessions end on that
normal restart. [1](#source-1) [3](#source-3) [4](#source-4)

[FACT] Sampling does not reserve blocks or impose a write-rate limit. Abrupt
external consumption can exhaust headroom between measurements. Filesystem
quotas, inode exhaustion and unrelated I/O failures are not modeled. A pause
retains a live process, not a durable VM snapshot: killing it or rebooting the
host still loses volatile state. Guest GC/TRIM remains separate and cannot run
while paused; this guard never deletes user files or other VMs.
[1](#source-1) [3](#source-3) [4](#source-4) [5](#source-5)

[FACT] Tests exercise both backing files, descriptor pinning, thresholds,
measurement/signal failures, monitoring cancellation, status and a disposable
writer's stop/continuation. The real signal test explicitly skips where a
sandbox denies pidfd signals; `YOLOCTL_TEST_REQUIRE_STORAGE_SIGNALS=1` makes
that denial a failure for the Linux host gate. The storage smoke also requires
a healthy guard endpoint in its isolated Firecracker fixture. These tests do
not establish that host exhaustion can never race the monitor.
[8](#source-8) [9](#source-9) [10](#source-10)

## Sources

1. <a id="source-1"></a> [Host storage guard](../../internal/firecracker/host_storage_linux.go#L23) — thresholds, pinned measurement, state machine, bounded read-only status.
2. <a id="source-2"></a> [Account disk creation](../../internal/firecracker/disk_linux.go#L48) — `CreateAccountDisk`, sparse allocation.
3. <a id="source-3"></a> [VM adapter](../../internal/firecracker/driver_linux.go#L37) — `List`, `Create`, `Start`, `Stop` and pause handling.
4. <a id="source-4"></a> [Helper lifetime](../../internal/firecracker/helper_linux.go#L163) — `Helper`, pinned backing disks and monitor lifetime.
5. <a id="source-5"></a> [Process identity](../../internal/firecracker/process_linux.go#L60) — `process.signal`, pidfd/start-time verification.
6. <a id="source-6"></a> [Host controller lifecycle](../../cmd/yoloctl/controller_deploy.go#L83) — controller admission/start/stop guards.
7. <a id="source-7"></a> [Space views](../../cmd/yoloctl/space.go#L1255) — human/JSON pause reason.
8. <a id="source-8"></a> [Guard tests](../../internal/firecracker/host_storage_linux_test.go#L26) — capacity, state transitions, socket and real child process.
9. <a id="source-9"></a> [Unavailable declarations view](../../cmd/yoloctl/space_storage_test.go#L11) — pause diagnostics without native data.
10. <a id="source-10"></a> [Storage runtime smoke](../../scripts/smoke/storage-gc.sh#L18) — isolated helper-status check and existing GC/data checks.
