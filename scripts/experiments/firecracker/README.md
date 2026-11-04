# Firecracker startup PoC

Target: a usable space in **1–2 seconds**, with lower memory use where possible.
This first experiment tests whether a smaller boot architecture provides enough
headroom for that target. It does not replace the yoloctl runtime backend.

**[FACT — implementation]** This directory adds a standalone host harness and a
Linux guest PID 1. The guest runs system containerd directly, followed by the
existing yoloctl controller. It boots without Lima, SSH, cloud-init, systemd or
boot-time downloads. Production CLI code and the installed binary are unchanged.

**[ASSUMPTION — to measure on KVM]** A fresh microVM or restored runtime template
can enforce OpenShell confinement and bring a newly initialized controller online
within 2 seconds. Neither an API
response from Firecracker nor an open guest socket proves this.

## Run on your Linux host

Use the `experiment/firecracker-poc` worktree. **Benchmark only:** x86_64 Linux,
read/write access to `/dev/kvm`, Python 3.11+, `mkfs.ext4`, and a prepared build.
Run as your normal user, without sudo. The harness does not install host
packages or change KVM permissions. Measurement is offline.

**Rebuilding:** preparation additionally needs this repository's Go toolchain,
`unsquashfs` (squashfs-tools), `debugfs` (e2fsprogs), and `dpkg-deb`. Building
the guest kernel needs GCC, make, tar, flex, bison, m4, bc, and the libelf/OpenSSL
development headers. Preparation needs network access unless all pinned inputs
are supplied through `--cache` and the Go dependencies are already available.
Allow several GB for the build and at least the configured guest RAM size plus
disks for the retained snapshot and trial artifacts. Full snapshots touch guest
RAM, so use a host with enough available memory.

```sh
cd .claude/worktrees/firecracker-poc
python3 -B scripts/experiments/firecracker/poc.py kernel --jobs 2
python3 -B scripts/experiments/firecracker/poc.py prepare
python3 -B scripts/experiments/firecracker/poc.py bench --samples 5 \
  --lima-report ../ubuntu-minimal-vm/yoloctl-startup.IXNN8q
```

If `yfc-build-ready/build.json` already exists from preparation in this worktree,
skip both `kernel` and `prepare`, and run `bench`. Existing build/trial directories are never
overwritten. To rebuild after source changes, pass `prepare --output yfc-build-v2`
and `bench --build yfc-build-v2`. Omit `--lima-report` if the old report is absent.

The default is **4 vCPUs / 4096 MiB**, matching the repository's Lima defaults.
For a separate memory experiment, repeat with `--memory-mib 512 --cpus 2`;
report that as a different resource profile, not a like-for-like comparison.

Every benchmark creates its own clean template and new trial directory. Nothing
is registered with yoloctl or Lima. Each test VM is terminated after its sample.
The harness prints the report location; inspect `report.json` and each sample's
`serial.log` on failure. An interrupted activation is never retried. Re-running
`bench` allocates entirely new state.

## What is measured

| Path | Work timed |
| --- | --- |
| Fresh | New data disk → new Firecracker process → kernel / PID 1 → containerd → OpenShell enforcement test → new space activation → controller read |
| Restore | New data disk → new Firecracker process → restore a clean runtime template → OpenShell enforcement test → new space activation → controller read |

The end condition requires all of these:

1. The running kernel returns Landlock ABI >= 3. The pinned OpenShell binary
   launches a finite Python probe with production's `best_effort` compatibility
   setting, default network and process supervisors, and the repository's Rego
   engine. Mandatory external checks reject missing enforcement; compatibility
   mode alone is never evidence of confinement. The probe verifies an
   allowed write/read, denied read/write outside its policy, UID/GID 2000,
   no effective capabilities, `no_new_privs`, seccomp and a different network
   namespace. A same-UID unconfined control first proves ordinary Unix file
   permissions permit the denied operations. This is a synthetic test, not an
   agent. The supervisor must also report successful nftables bypass-rule and
   TLS CA setup. This does not test external traffic policy. The host rejects a
   result without this gate passing after boot/restore.
2. `nerdctl info` succeeds against system containerd.
3. A dedicated ext4 disk is mounted; the existing native-account helper creates
   its ownership marker, checks filesystem UUID and creates the verified bind.
4. The real `controller-internal bootstrap`, `initialize`, `activate` and `serve`
   paths run. Initialization and activation retain their production fsync rules.
5. A real `account.list --json` controller RPC passes the exact
   protocol/bundle/space/generation handshake and returns only `trial`.
6. Containerd responds again after the controller read. The host validates the
   fresh request nonce, generation and expected inventory.

`request_to_controller_ready_ms` includes trial allocation and data-disk
formatting. `process_to_controller_ready_ms` starts immediately before spawning
Firecracker; `runtime_ready_ms` ends before space activation. Guest step timings
are cumulative from activation. Preparation/downloads and template creation
are reported separately, outside the repeated ready-path timer.

Fresh and restored samples alternate order. Host caches are **left intact**;
artifact checksum verification and template creation warm them. “Fresh” means a
newly booted VM, not a cold host filesystem cache. There is no background warm
VM pool and no privileged host cache dropping. Slow samples and failures are
not discarded. The report includes min/median/max and whether **every** sample
passed the 2-second controller target; this is not a tail-latency claim.

Memory fields include guest `MemTotal - MemAvailable` at readiness and host VMM
RSS/PSS at readiness and after a short settle. Host VMM memory includes resident
guest pages; it is not an estimate of hypervisor-only overhead. Snapshot file
logical/allocated sizes are separate. A full snapshot faults guest memory;
restore uses demand paging, and timing includes pages touched by the controller
read. Unused box/image pages are not tested.

The optional historical Lima report is read only. It supplies lifecycle timings
for context, with an explicit different-scope label. The prior startup report
measures the Lima CLI lifecycle; this PoC omits capabilities that Lima provides.
Do not present their ratio as a proven full-product speedup.

## Isolation and snapshot boundary

**[FACT — implementation]** All writes go to new build/trial directories or
private short-lived `/tmp/yfc-*` socket directories. No installed `yoloctl`,
`limactl`, Docker or host containerd command is invoked. No host filesystem is
mounted, no TAP device is created, and no host route, firewall, service or
configuration directory is changed. Cleanup stops only the process handle
created for that sample. Disks, logs and reports are retained in the worktree.

The root filesystem is read-only. Runtime writes live in guest tmpfs. The
template contains running containerd and the experiment's vsock listener, but
**no activated controller, declared account, space identity or mounted data filesystem**.
A blank fixed-size data drive is present when the template boots. Every clone
resolves that relative drive path to a different newly formatted disk; the guest
invalidates block-device probe caches before mounting it. There is no device
hotplug or reuse of an active account disk. Every clone receives new host-generated
disk identity, filesystem UUID, generation, nonce and wall-clock time.

Snapshots are made and used on the same host with the same Firecracker binary,
CPU configuration and read-only rootfs. The source VM is paused, a full snapshot
is fsynced, and the source process is stopped before restore tests. Existing
vsock connections are not reused. The listener accepts only host CID 2, through
a Unix socket in a mode-0700 directory. This is trusted synthetic infrastructure,
not an API to expose to workloads or other users.

Firecracker does not package disk snapshots automatically, and snapshot
portability has CPU/version constraints. The clean unmounted-disk boundary and
read-only rootfs avoid trying to pair arbitrary live filesystem state with a
restored memory image. See the pinned upstream
[snapshot documentation](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/snapshotting/snapshot-support.md)
and [vsock protocol](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/vsock.md).

## Scope limits and next decision

**[FACT — scope]** This is an OpenShell/controller/runtime boot gate,
not a usable-box smoke gate. The OpenShell probe runs directly inside the
disposable guest, not a managed yoloctl box. It has no host project share,
external network connectivity, persistent session or agent authentication. `/poc-workdir`
is a synthetic guest-local directory, not a host mount. Catalog overrides are
empty. No user accounts or credentials are copied into the image/template.

**[FACT — workaround]** The originally examined 6.1 demo kernel disabled
Landlock. It is rejected by this harness. A newer prebuilt 6.18 image supplied
Landlock but lacked nftables options used by OpenShell. The workaround is a
custom guest kernel: pinned Amazon Linux **6.18.25** microVM sources and
[Firecracker's 6.18 configuration](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/resources/guest_configs/microvm-kernel-ci-x86_64-6.18.config),
with Landlock, nftables inet/reject/log/limit and kernel-config inspection enabled.
The fork's optional `CRYPTO_FIPS140_EXTMOD` module is explicitly disabled: with
this configuration it failed to load during the software boot test. No FIPS
validation is claimed.
Firecracker 1.17 [supports this guest series and recommends the microVM kernel fork](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/kernel-policy.md).
Preparation verifies the config and artifact hashes; guest syscalls and actual
OpenShell denial tests verify enforcement. The `kernel` command performs this
one-time build; it is outside startup measurements.

The test uses production's Landlock compatibility setting because the pinned
OpenShell/library combination rejects directory-only rights on device-file
rules in `hard_requirement` mode. The same-UID denial checks remain mandatory.
The repository pins OpenShell v0.0.72, whose
[Landlock implementation targets ABI 2](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-process/src/sandbox/linux/landlock.rs#L150-L173).
This experiment additionally requires the kernel to provide ABI >= 3; that
does not change which rights the pinned OpenShell implementation enforces.
The Ubuntu filesystem is a convenient fixture, not a production hardened
or maximally small image. Only startup orchestration is simplified here.

The PoC omits the production jailer/cgroup lifecycle, comprehensive PID 1
supervision, clone entropy/security review, host-workdir transport, networking,
recovery and update management. It is Linux/x86_64 only. Do not run real agents
or inject secrets into it. No production portability or isolation equivalence
is claimed.

**[DERIVED]** A result above 2 seconds fails this experiment's target; inspect
the step timings, including the synthetic enforcement check, before attributing
that cost to a production design. If it is well below 2 seconds, the next
experiment should spend the remaining budget on one real OpenShell-confined
box and project access. Only that end-to-end gate
can establish the UX target. Mac support remains a separate backend question.

## Reproducibility and local checks

**[FACT — local validation, 2026-10-07]** The prepared `yfc-build-ready` image
passed `software-smoke` under QEMU TCG: kernel Landlock ABI 7; OpenShell allowed
and denied filesystem operations as expected; UID/capability/seccomp/netns checks
passed; nftables and TLS setup reported success; native ext4 initialization and
the real controller account-list RPC passed. The retained evidence is
`yfc-software-candidate/report.json` and `serial.log` (ignored local artifacts).
The image was renamed from candidate to ready without modifying its contents.
The guest logged an unavailable PID limit and a network-namespace teardown
warning; resource-limit enforcement and reusable namespace cleanup are outside
this finite probe's coverage. Every test VM is terminated by the harness.

**[FACT — host validation, results received 2026-10-08]** The user's Linux KVM
run passed all five fresh boots and five snapshot restores. The inspected
`yfc-trial-1c39a6b7/report.json` contains the same build manifest as
`yfc-build-ready/build.json`; every sample reports Landlock ABI 7 and a passing
OpenShell/controller gate. Resources were 4 vCPUs / 4096 MiB, with host caches
left intact. These five samples per path do not establish tail latency.

| Measurement | Fresh | Restore |
| --- | ---: | ---: |
| Request to controller, median | 1095.253 ms | 1009.544 ms |
| Request to controller, maximum | 1373.529 ms | 1083.041 ms |
| Process to runtime ready, median | 516.788 ms | 10.621 ms |
| Guest activation through final runtime check, median | 538.167 ms | 924.457 ms |
| Guest used memory at ready, median | 268.68 MiB | 264.84 MiB |
| Host VMM PSS at ready, median | 372.89 MiB | 225.32 MiB |

The median request-to-controller saving is 85.709 ms (7.83%). The largest
observed activation difference is the store initialization/activation step:
197.533 ms fresh versus 471.192 ms restored, using medians of each sample's
step duration. The clean template took 10.285 s to prepare and its memory
snapshot allocated approximately 4 GiB; preparation is outside repeated startup.
VMM PSS excludes host page-cache costs outside the process and is not total
host memory consumption. The report does not explain the activation slowdown;
snapshot page faults or storage effects remain unverified hypotheses.

**[DERIVED]** Fresh boot is the simpler starting point for the next usable-box
experiment: it already passes this limited 2-second gate, while snapshots add
template management for a modest observed overall saving. Both paths still
need a real managed OpenShell box, project access and an interactive command
before they can establish the requested UX. Historical Lima lifecycle timings
remain a different-scope comparison, not a proven full-product speedup.

**[FACT — remaining validation]** `/dev/kvm` is absent in the coding environment;
the live Firecracker evidence above comes from the user's host run.
QEMU TCG timings are not performance evidence for Firecracker. The maintained
Lima live smoke was not run for this standalone experiment. Host unit tests,
Go tests/vet/lint and documentation checks passed; the maintained package check
also passed during development. No usable-box result or memory saving is claimed.

Inputs are pinned in [pins.json](../../../assets/vm/firecracker/pins.json): Firecracker 1.17.0, nerdctl-full
2.3.5, repository-pinned OpenShell v0.0.72, Firecracker CI's Ubuntu 24.04
filesystem, Ubuntu nftables packages, and the kernel source/config above. Release archive
hashes come from GitHub release asset metadata. Fixture hashes were measured
from the upstream HTTPS downloads; these are integrity pins, not publisher
signatures. Mutable upstream replacement fails checksum validation. The build
manifest records input hashes, source revision, unique coordinated build ID,
guest/controller hashes and final image hashes; the benchmark rechecks them.

Rootless `unsquashfs` extraction excludes device nodes. `mkfs.ext4 -d` and
`debugfs` build and normalize root-owned guest inodes without host mounts.
Critical root-owned paths are checked in the finished image. No host packages
are installed by `prepare`; `--unsquashfs PATH` can select a private tool.

```sh
python3 -B -m unittest discover -s scripts/experiments/firecracker -v
go test ./scripts/experiments/firecracker/guest
go vet ./scripts/experiments/firecracker/guest
make check 'PKG=./assets/... ./cmd/... ./internal/... ./scripts/experiments/firecracker/guest'
```

These checks cover identity validation, current store-format compatibility,
protocol/error handling, owned-process cleanup, overwrite refusal and missing
KVM preflight. They do **not** validate KVM boot or snapshot restoration.
The live `bench` command is the PoC's runtime gate; the maintained Lima smoke
suite does not exercise this backend.

When KVM is unavailable but `qemu-system-x86_64` is present, a bounded functional
check can boot the same kernel/rootfs with software emulation:

```sh
python3 -B scripts/experiments/firecracker/poc.py software-smoke --build yfc-build-ready
```

It runs the OpenShell gate, initializes a fresh native disk, starts the real
controller, validates an account-list RPC and powers the VM off. Use
`--security-only` to limit it to the confinement checks. A pass establishes those
guest functions under QEMU TCG, **not** Firecracker boot, snapshot restore,
managed-box integration or startup performance. It creates no
host networking. Private QEMU distributions can supply `--qemu`, `--qemu-data`
and their package-specific library/module environment paths.
