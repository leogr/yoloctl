# Firecracker host runtime

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9`; preparation progress at `c2dc67262d5a68d91511d74d7ec3262de1c1caab` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Firecracker runs as the invoking user with KVM access. One private helper
per VM supplies userspace networking, fixed-grant SFTP and loopback port
forwarding. The port digest records discovery, SSH reuse and the TCP half-close
compatibility boundary. [1](#source-1) [2](#source-2)
Go SSH clients verify the selected VM key and propagate
stream bytes, EOF, PTY resize and exit status without a host SSH executable.
PTY requests preserve measured window pixels, including unknown zero values.
[3](#source-3) No host service, autostart, TAP device or privileged network setup is installed.
Private sockets and pinned SSH keys authenticate transport.
[1](#source-1) [4](#source-4) [2](#source-2) [5](#source-5) [6](#source-6)
The Firecracker vsock acknowledgment and SSH login use the caller's deadline
and cancellation. [5](#source-5) [6](#source-6) [17](#source-17)
`box exec` uses the same native client over its controller-authorized OpenShell
proxy. [7](#source-7)

[FACT] Firecracker start retries the guest ready-file probe with ten seconds
per attempt and a two-minute output inactivity window. Validated current
private log growth or a nonempty rotated generation renews it; retained history,
empty replacement and truncation alone do not. Helper exit, a proved dead VMM
owned by this launch, and caller cancellation remain distinct failures. Storage
pause retains the live runtime before destructive cleanup. Noisy output can
prolong waiting; silence can expire without proving a hung guest.
[8](#source-8) [4](#source-4) [9](#source-9)

[FACT] The private helper gives its Firecracker API socket wait and each API
request a separate two-minute fail-safe. The socket wait also ends on child
exit, and both waits respect the helper's signal context. Ordinary VM stop
follows the caller and observes termination after a
lost poweroff reply. When boot fails, settlement keeps the exact helper's
five-second TERM grace. The post-KILL join and the subsequent forced stop
each get two minutes. The original boot cause and the existing ownership and
storage checks still apply. [1](#source-1) [4](#source-4) [8](#source-8) [18](#source-18)

[FACT] Before the VMM exists, the helper's preparation writes its own records
into that log: begin and end of the guest artifact hash (resolved once for the
boot disk and the system-disk seed), a data-disk format, the seed build, verify
and publish, and the boot disk build. While reads or writes complete, a byte
count follows at most every 30 s. There is no heartbeat, so a blocked read,
write or fsync still reaches the inactivity window. [10](#source-10)

[FACT] Each host grant is served from its own `os.Root`; the share digest
records read-only, symlink, descriptor and parent-sync limits. SSHFS disables
caching so catalog snapshots observe replacements. Native accounts and
declarations are never transported through these shares. [11](#source-11) [12](#source-12)

[FACT] Writable raw disks use synchronous IO, `Writeback` flush semantics and
virtio discard. The existing controller maintenance worker trims only the system
ext4 mount and verified native account mount. Sparse allocation grows with
writes; capacity is fixed and no public resize command exists. Reclamation
requires host filesystem hole-punch support. [13](#source-13) [14](#source-14)

[FACT] Helper/VMM records include PID and process start time; signals use pidfds.
An unproven interrupted launch prevents safe-stop proof. Formatting requires all-zero allocated extents. Lifecycle checks UUID, ownership,
mounts, controller bundle and generation. [8](#source-8) [15](#source-15) [16](#source-16)

## Sources

1. <a id="source-1"></a> [Per-VM helper](../../internal/firecracker/helper_linux.go#L1) — `Helper`, `Proxy`
2. <a id="source-2"></a> [Port forwarding](vm-ports.md) — discovery, transport and half-close compatibility.
3. <a id="source-3"></a> [Unix SSH transport](../../internal/sshtransport/client_unix.go#L1) — measured PTY geometry and inherited proxy sockets.
4. <a id="source-4"></a> [adapter](../../internal/firecracker/driver_linux.go#L1) — `Start`, `Stop`, `shell`
5. <a id="source-5"></a> [Native SSH](../../internal/firecracker/ssh_linux.go#L1) — selected VM host identity and private key.
6. <a id="source-6"></a> [SSH transport](../../internal/sshtransport/client.go#L1) — `Run`, `Connect`, bounded handshake, stream/PTY/exit handling.
7. <a id="source-7"></a> [Box exec](../../cmd/yoloctl/run.go#L95) — controller-authorized OpenShell SSH proxy.
8. <a id="source-8"></a> [Process proof](../../internal/firecracker/process_linux.go#L1) — `process`, `running`
9. <a id="source-9"></a> [Boot activity](../../internal/firecracker/boot_activity_linux.go#L1) — `bootActivity`, `bootLogStat`; validated rotating generations.
10. <a id="source-10"></a> [Preparation progress](../../internal/firecracker/prep_progress_linux.go#L1) — `prepReport`, `prepProgress`, `withPrepProgress` (counted in `contextDisk.WriteAt`), `prepReader`; `guestbinary.ResolveReporting`; the stages in `Helper`/`bootImage` (helper_linux.go) and seed_linux.go; `TestPrepRecordsFeedTheBootWatchUntilSilence`.
11. <a id="source-11"></a> [Host share evidence](vm-shares.md) — typed handles, creation modes, synchronization and limits
12. <a id="source-12"></a> [guest mounts](../../internal/vmguest/init_linux.go#L1) — `share`.
13. <a id="source-13"></a> [Drive configuration](../../internal/firecracker/drives_linux.go#L1) — `driveConfig`.
14. <a id="source-14"></a> [Guest maintenance](../../cmd/yoloctl/controller_storage.go#L1) — `collectControllerStorage`, `trimGuestStorage`.
15. <a id="source-15"></a> [disk safety](../../internal/firecracker/disk_linux.go#L1) — `blank`, `formatDisk`
16. <a id="source-16"></a> [deployment](../../cmd/yoloctl/controller_deploy.go#L139) — `startControllerSpace`.
17. <a id="source-17"></a> [Vsock acknowledgment](../../internal/firecracker/api.go#L52) — `DialGuest`.
18. <a id="source-18"></a> [Bootstrap API](../../internal/firecracker/api.go#L17) — `API`, `apiRequest`.
