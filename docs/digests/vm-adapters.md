# VM adapters and the Firecracker guest

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `internal/vmapi.Driver` separates VM lifecycle, owned disks, shell/copy
transport, process termination proof and the fixed controller service from
controller operations. `internal/vm` selects the persisted bootstrap backend.
Empty/legacy selection means Lima; Firecracker is registered only on Linux
amd64. Reads never boot VMs. Host lookup uses only the selected adapter,
rejecting conflicting foreign metadata; guest lookup reports only its own space. [1](#source-1)
[2](#source-2) [11](#source-11)

[FACT] New Linux amd64 spaces default to Firecracker; other builds default to
Lima. Empty persisted backends remain Lima. Image preparation, verification and cache ownership are documented separately.
[2](#source-2) [25](#source-25)

[FACT] Lima boot preflight resolves the selected home (including existing-home
symlinks), adds `yolo-<space>/ssh.sock.1234567890123456` and requires its byte
length below 104 on Darwin or 108 on Linux, matching Lima's OpenSSH temporary
socket allowance. [35](#source-35) [36](#source-36) [37](#source-37)
Create/import reject before destination publication or VM/disk
effects; start checks before the compatibility offer and again under the
lifecycle lock, after installer consent. Recreate checks before teardown;
deployment and low-level Lima create/start also check. Errors identify the
path, byte count and limit. [38](#source-38) [39](#source-39)
[40](#source-40) [42](#source-42)
This optional adapter check leaves Firecracker,
identifier syntax, reads and cleanup unchanged. [41](#source-41)

## Lima stack upgrades

[FACT] Existing Lima spaces adopt the pinned full stack through an explicit
host controller update, space recreation and selected box starts. The stack
digest records the commands, override/drift limits and preserved/lost state;
the host runner separates isolated acceptance from read-only maintenance
observations. [34](#source-34)

## Firecracker runtime

[FACT] Firecracker runs as the invoking user with KVM access. One private helper
per VM supplies userspace networking, fixed-grant SFTP and loopback port
forwarding. The port digest records discovery, SSH reuse and the TCP half-close
compatibility boundary. [3](#source-3) [13](#source-13)
Go SSH clients verify the selected VM key and propagate
stream bytes, EOF, PTY resize and exit status without a host SSH executable.
PTY requests preserve measured window pixels, including unknown zero values.
[32](#source-32) No host service, autostart, TAP device or privileged network setup is installed.
Private sockets and pinned SSH keys authenticate transport.
[3](#source-3) [12](#source-12) [13](#source-13) [29](#source-29) [30](#source-30)
`box exec` uses the same native client over its controller-authorized OpenShell
proxy. [31](#source-31)

[FACT] Each host grant is served from its own `os.Root`; the share digest
records read-only, symlink, descriptor and parent-sync limits. SSHFS disables
caching so catalog snapshots observe replacements. Native accounts and
declarations are never transported through these shares. [4](#source-4) [14](#source-14)

[FACT] The guest has a dedicated PID 1, system containerd and SSH;
it does not run systemd or first-boot package installation. BuildKit starts on
the first image build, once per VM lifetime; failures remain explicit. Its containerd worker uses nerdctl's `default` namespace for local base images. [24](#source-24)
Its cache root is `/var/lib/buildkit/default`. The guest configures shared GC
budgets before readiness without starting BuildKit. [24](#source-24) [26](#source-26) Guest
protocol 4 requires a rebuilt image. [20](#source-20) The immutable root
has a disposable ext4 upper filesystem. Containerd/BuildKit bind separate directories on that disk outside the OS overlay. A separate ext4 disk retains accounts and declarations.
Landlock ABI >= 3 is mandatory; the shared OpenShell launch and policy paths
remain authoritative for workloads. [5](#source-5) [6](#source-6)

[FACT] Lima uses
systemd; Firecracker uses root-only fixed supervisor requests for controller,
holder and port services. Holders have fixed names and never automatically
restart. Controller failure-only retries, their episode limit and diagnostics,
stop joining and VM-image rollout are recorded separately. [33](#source-33)
Each incarnation gets a fresh cgroup, and cleanup verifies it is empty.
This avoids the upstream `CLONE_INTO_CGROUP`/`cgroup.kill` bug. [7](#source-7) [15](#source-15) [8](#source-8)

[FACT] Writable raw disks use synchronous IO, `Writeback` flush semantics and
virtio discard. The existing controller maintenance worker trims only the system
ext4 mount and verified native account mount. Sparse allocation grows with
writes; capacity is fixed and no public resize command exists. Reclamation
requires host filesystem hole-punch support. [27](#source-27) [28](#source-28)

[FACT] Helper/VMM records include PID and process start time; signals use pidfds.
An unproven interrupted launch prevents safe-stop proof. Formatting requires all-zero allocated extents. Lifecycle checks UUID, ownership,
mounts, controller bundle and generation. [9](#source-9) [16](#source-16) [17](#source-17)

[FACT] Deployment groups native-disk verification, store activation and service
startup. Native box readiness uses a bounded Unix-socket SSH greeting probe. Container stop signals all task processes before
the bounded nerdctl stop, allowing the OpenShell supervised child to exit.
The benchmark measures a managed prompt by default (`--readiness exec` selects
command readiness); implementation alone proves no timing target. [21](#source-21) [22](#source-22)
[20](#source-20) [23](#source-23)

[FACT] The QEMU TCG gate uses TCP and a 90-second holder bound, not production vsock
and 10 seconds. It tests local/derived/composed builds and confined execution,
not Firecracker/KVM behavior. The adapter boots fresh; snapshots remain experimental. [10](#source-10) [18](#source-18) [19](#source-19)

## Sources

1. <a id="source-1"></a> [VM boundary](../../internal/vmapi/driver.go#L10) — `Driver`.
2. <a id="source-2"></a> [Adapter selection](../../internal/vm/vm.go#L1) — `Driver`, `ForSpace`, `lookup`, shell and lifecycle routing
3. <a id="source-3"></a> [Per-VM helper](../../internal/firecracker/helper_linux.go#L1) — `Helper`, `Proxy`
4. <a id="source-4"></a> [Host share evidence](vm-shares.md) — typed handles, creation modes, synchronization and limits
5. <a id="source-5"></a> [Guest boot](../../internal/vmguest/init_linux.go#L1) — `boot`, `writableRoot`, Landlock and runtime readiness.
6. <a id="source-6"></a> [Image assembly](../../internal/firecracker/assemble_linux.go#L1) — `assembleLocalImage`, dependency and kernel verification.
7. <a id="source-7"></a> [Service selection](../../internal/vmservice/service.go#L1) — `microvm`, `Start`, `Stop`
8. <a id="source-8"></a> [Linux fix 8e359920](https://github.com/torvalds/linux/commit/8e359920216689b3b79e0fe8961a77fe312a511f) — cgroup kill-sequence mismatch for `CLONE_INTO_CGROUP` children.
9. <a id="source-9"></a> [Process proof](../../internal/firecracker/process_linux.go#L1) — `process`, `running`
10. <a id="source-10"></a> [Software gate](../../internal/firecracker/software_linux_test.go#L1) — `TestSoftwareVM`
11. <a id="source-11"></a> [registration](../../internal/vm/firecracker_linux_amd64.go#L1) .
12. <a id="source-12"></a> [adapter](../../internal/firecracker/driver_linux.go#L1) — `Start`, `Stop`, `shell`
13. <a id="source-13"></a> [Port forwarding](vm-ports.md) — discovery, transport and half-close compatibility.
14. <a id="source-14"></a> [guest mounts](../../internal/vmguest/init_linux.go#L1) — `share`.
15. <a id="source-15"></a> [guest units](../../internal/vmguest/units_linux.go#L1) — `validateUnit`, `prepareGroup`, `cleanGroup`, `manageUnit`.
16. <a id="source-16"></a> [disk safety](../../internal/firecracker/disk_linux.go#L1) — `blank`, `formatDisk`
17. <a id="source-17"></a> [deployment](../../cmd/yoloctl/controller_deploy.go#L139) — `startControllerSpace`.
18. <a id="source-18"></a> [test-only carrier](../../internal/vmguest/transport_software_linux.go#L1).
19. <a id="source-19"></a> [experiment](../../scripts/experiments/firecracker/poc.py#L1) .

20. <a id="source-20"></a> [Container operations](../../internal/container/container.go#L1) — `EnsureBuilder`, `Build`, `Stop`.
21. <a id="source-21"></a> [Deployment](../../cmd/yoloctl/controller_deployment.go#L1) — `deployController`.
22. <a id="source-22"></a> [Readiness](../../internal/agentbox/readiness.go#L1) — `WaitForSandboxSocket`, `probeSandboxSSH`.
23. <a id="source-23"></a> [Benchmark](../../scripts/vm/firecracker/benchmark.py#L1) — interactive and exec readiness endpoints, stop timings and memory accounting.
24. <a id="source-24"></a> [Build service](../../internal/vmguest/init_linux.go#L522-L541) — `ensureBuilder`, explicit worker namespace and namespace-specific cache root.

25. <a id="source-25"></a> [Local VM image preparation](vm-images.md) — recipe, verification and host cache boundaries.
26. <a id="source-26"></a> [Guest build policy](../../internal/vmguest/buildkit_linux.go#L1) — `configureBuilder`.
27. <a id="source-27"></a> [Drive configuration](../../internal/firecracker/drives_linux.go#L1) — `driveConfig`.
28. <a id="source-28"></a> [Guest maintenance](../../cmd/yoloctl/controller_storage.go#L1) — `collectControllerStorage`, `trimGuestStorage`.

29. <a id="source-29"></a> [Native SSH](../../internal/firecracker/ssh_linux.go#L1) — selected VM host identity and private key.
30. <a id="source-30"></a> [SSH transport](../../internal/sshtransport/client.go#L1) — `Run`, `Connect`, bounded handshake, stream/PTY/exit handling.
31. <a id="source-31"></a> [Box exec](../../cmd/yoloctl/run.go#L95) — controller-authorized OpenShell SSH proxy.

32. <a id="source-32"></a> [Unix SSH transport](../../internal/sshtransport/client_unix.go#L1) — measured PTY geometry and inherited proxy sockets.

33. <a id="source-33"></a> [Controller supervision](controller-supervision.md) — process restart policy and limits.

34. <a id="source-34"></a> [Lima stack](lima-stack.md) — explicit upgrade, preservation and QA boundary.

35. <a id="source-35"></a> [Lima preflight](../../internal/lima/socket_paths.go#L21) — `ValidateInstancePaths`, `validateInstanceSocketPath`.
36. <a id="source-36"></a> [Lima v2.2.1 filenames](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/limatype/filenames/filenames.go#L97-L105) — `LongestSock` includes OpenSSH's temporary suffix.
37. <a id="source-37"></a> [Lima v2.2.1 creation](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/instance/create.go#L33-L43) — strict `UnixPathMax` bound.
38. <a id="source-38"></a> [Host lifecycle](../../cmd/yoloctl/controller_lifecycle.go#L91) — `hostControllerLifecycle`, `createControllerSpace`.
39. <a id="source-39"></a> [Import](../../cmd/yoloctl/space_archive.go#L183) — `importSpace` preflight before staging/publication.
40. <a id="source-40"></a> [Deployment](../../cmd/yoloctl/controller_deploy.go#L167) — `startControllerSpaceImpl` before compatibility/disk work.
41. <a id="source-41"></a> [Adapter dispatch](../../internal/vm/vm.go#L83) — optional `ValidateInstancePaths`.
42. <a id="source-42"></a> [Lima entry points](../../internal/lima/lima.go#L41) — `Client.Create`, `Client.Start` before subprocesses.
