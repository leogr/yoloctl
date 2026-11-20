# Firecracker guest and its services

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9`; re-read cited sources when changing behavior.

[FACT] The guest has a dedicated PID 1, system containerd and SSH;
it does not run systemd or first-boot package installation. BuildKit starts lazily on
an image-build request; an exited daemon can restart on a later explicit
request, while failures remain explicit. Guest
lazy-builder readiness and boot containerd readiness each use a two-minute
budget; the latter cancels an in-flight nerdctl probe. Only decoded buildkit
control requests extend their reply deadline: after at most two minutes of
work, an ordinary 30-second write grace delivers success or timeout. Request
intake and other control actions retain their 30-second deadline. Its containerd worker uses nerdctl's `default` namespace for local base images. [1](#source-1) [2](#source-2)
Its cache root is `/var/lib/buildkit/default`. The guest configures shared GC
budgets before readiness without starting BuildKit. [2](#source-2) [3](#source-3) Guest
protocol 4 requires a rebuilt image. [4](#source-4) The immutable root
has a disposable ext4 upper filesystem. Containerd/BuildKit bind separate directories on that disk outside the OS overlay. A separate ext4 disk retains accounts and declarations.
Landlock ABI >= 3 is mandatory; the shared OpenShell launch and policy paths
remain authoritative for workloads. [1](#source-1) [5](#source-5)

[FACT] Lima uses
systemd; Firecracker uses root-only fixed supervisor requests for controller,
holder and port services. Holders have fixed names and never automatically
restart. Controller failure-only retries, their episode limit and diagnostics,
stop joining and VM-image rollout are recorded separately. [6](#source-6)
Each incarnation gets a fresh cgroup, and cleanup verifies it is empty.
This avoids the upstream `CLONE_INTO_CGROUP`/`cgroup.kill` bug. [7](#source-7) [8](#source-8) [9](#source-9)

[FACT] Deployment groups native-disk verification, store activation and service
startup. Native box readiness uses a bounded Unix-socket SSH greeting probe. Container stop signals all task processes before
the bounded nerdctl stop, allowing the OpenShell supervised child to exit.
The benchmark measures a managed prompt by default (`--readiness exec` selects
command readiness); implementation alone proves no timing target. [10](#source-10) [11](#source-11)
[4](#source-4) [12](#source-12)

[FACT] The QEMU TCG gate uses TCP and a 90-second holder bound, not production vsock
and 10 seconds. It tests local/derived/composed builds and confined execution,
not Firecracker/KVM behavior. The adapter boots fresh; snapshots remain experimental. [13](#source-13) [14](#source-14) [15](#source-15)

## Sources

1. <a id="source-1"></a> [Guest boot](../../internal/vmguest/init_linux.go#L1) — `boot`, `writableRoot`, Landlock/runtime readiness, `waitForContainerd`, `probeContainerd` and `serveControl`.
2. <a id="source-2"></a> [Build service](../../internal/vmguest/buildkit_linux.go#L78) — `ensureBuilder`, explicit worker namespace and namespace-specific cache root.
3. <a id="source-3"></a> [Guest build policy](../../internal/vmguest/buildkit_linux.go#L1) — `configureBuilder`.
4. <a id="source-4"></a> [Container operations](../../internal/container/container.go#L1) — `EnsureBuilder`, `Build`, `Stop`.
5. <a id="source-5"></a> [Image assembly](../../internal/firecracker/assemble_linux.go#L1) — `assembleLocalImage`, dependency and kernel verification.
6. <a id="source-6"></a> [Controller supervision](controller-supervision.md) — process restart policy and limits.
7. <a id="source-7"></a> [Service selection](../../internal/vmservice/service.go#L1) — `microvm`, `Start`, `Stop`
8. <a id="source-8"></a> [guest units](../../internal/vmguest/units_linux.go#L1) — `validateUnit`, `prepareGroup`, `cleanGroup`, `manageUnit`.
9. <a id="source-9"></a> [Linux fix 8e359920](https://github.com/torvalds/linux/commit/8e359920216689b3b79e0fe8961a77fe312a511f) — cgroup kill-sequence mismatch for `CLONE_INTO_CGROUP` children.
10. <a id="source-10"></a> [Deployment](../../cmd/yoloctl/controller_deployment.go#L1) — `deployController`.
11. <a id="source-11"></a> [Readiness](../../internal/agentbox/readiness.go#L1) — `WaitForSandboxSocket`, `probeSandboxSSH`.
12. <a id="source-12"></a> [Benchmark](../../scripts/vm/firecracker/benchmark.py#L1) — interactive and exec readiness endpoints, stop timings and memory accounting.
13. <a id="source-13"></a> [Software gate](../../internal/firecracker/software_linux_test.go#L1) — `TestSoftwareVM`
14. <a id="source-14"></a> [test-only carrier](../../internal/vmguest/transport_software_linux.go#L1).
15. <a id="source-15"></a> [experiment](../../scripts/experiments/firecracker/poc.py#L1) .
