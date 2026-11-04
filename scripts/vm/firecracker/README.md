# Firecracker adapter

This Linux amd64 adapter uses KVM and a prepared image. It installs no host
daemon, service or autostart entry. Each VM has one private helper, launched by
its `yoloctl` executable, for userspace network and project-file transport. The
helper exits with its VM. No host TAP, root networking or host controller is
required. Lima remains available on supported platforms.

New Linux amd64 spaces default to this adapter. Other hosts default to Lima;
`--backend lima` explicitly selects it. Empty backend records in existing spaces
remain Lima. The adapter boots fresh; snapshot experiments remain in
[the PoC harness](../../experiments/firecracker/README.md).

## Current limits

These are implemented boundaries, not a roadmap or a claim of validation on
every host. The linked evidence describes the current code and contracts.

| Area | Current limit |
|---|---|
| VM image updates | Explicit `space recreate --update-vm-image` or `--vm-image DIR`; same-build retry or compatible previous-image recovery while pending. Native data is retained, not snapshotted. [Image evidence](../../../docs/digests/vm-images.md). |
| Platforms | This adapter supports Linux amd64 with KVM. Other supported hosts use Lima; there is no separate yoloctl Apple Virtualization adapter. [Adapter selection](../../../internal/vm/vm.go), [Firecracker registration](../../../internal/vm/firecracker_linux_amd64.go). |
| Project files and ports | Host shares use SSHFS; absolute/escaping symlinks are unsupported. Published ports bind to host loopback. [Adapter evidence](../../../docs/digests/vm-adapters.md). |
| Disk capacity | Sparse disks and TRIM reclaim host allocation; maximum guest capacity stays fixed. No public disk-resize command exists. [Storage details](#storage-and-compatibility). |
| Backups | Export requires stopped workloads and captures declarations/account state. Host projects, catalogs, VM/container images, ephemeral workdirs and sessions are excluded. It is not a live workload snapshot. [Archive contract](../../../docs/digests/space-archives.md). |
| Snapshot restore | The integrated adapter boots fresh. Restore remains in the experiment harness. [Adapter evidence](../../../docs/digests/vm-adapters.md). |
| Artifact distribution | Local preparation and explicit image bundles are supported. There is no unified OCI artifact resolver/publisher. [Image evidence](../../../docs/digests/vm-images.md). |

## First use and local storage

Run as your ordinary user with read/write access to `/dev/kvm`. Install all four
files from one `make build`: the host CLI, both Linux CLI companions and
`yoloctl-vm-init-linux-amd64`. No source checkout or Go compiler is needed at
runtime. The host executable includes a prepared Landlock kernel. First-use
image assembly, disk formatting and SSH transport run in Go; no Python, compiler,
Debian packages, filesystem utilities or OpenSSH installation is required.
No host packages, services or build-tool cache are installed.

```sh
./bin/yoloctl space create fc-trial --account trial --workdir /path/to/project
# Explicit Lima remains available:
./bin/yoloctl space create lima-trial --account trial --backend lima --workdir /path/to/project
```

Creation automatically prepares a local image when no compatible image exists.
It downloads checksum-pinned upstream inputs and assembles the guest filesystem
using the matching init companion and bundled kernel. This first
preparation can take several minutes. Concurrent creates share an installation
lock; cancellation interrupts downloads and assembly. Failed builds never
publish a default. Network access is unnecessary once all inputs are cached.

On Linux, finished immutable media lives in
`$XDG_CONFIG_HOME/yoloctl/vm-images/firecracker` (default `~/.config/yoloctl/...`).
Reusable downloads and logs live in
`$XDG_CACHE_HOME/yoloctl/vm-build/firecracker` (default `~/.cache/yoloctl/...`).
Build-cache removal cannot remove in-use boot media. Failed preparation retains
its log; temporary extraction files are removed. Host grants cannot expose these
managed paths. No remote yoloctl image release or OCI registry is required.

`--vm-image DIR` is an optional verified override; it does not replace the
automatic default. Automatic cache identity includes recipe version, init hash,
kernel/package inputs and guest protocol. Images use guest protocol 4.
Older trial images must be rebuilt; rebuilding only the CLI does not replace
an existing VM's immutable media. Existing spaces never silently switch images.
For maintainers, the older tool-based image builder and kernel compiler remain
available for experiments. Their development prerequisites are not user runtime
requirements:

```sh
python3 scripts/vm/firecracker/linux7.py kernel --output yfc-kernel-runtime
python3 scripts/vm/firecracker/build.py --kernel-build yfc-kernel-runtime --output yfc-runtime-ready
# Update the kernel asset after reviewing its source/configuration changes:
python3 scripts/vm/firecracker/bundle-kernel.py --kernel-build yfc-kernel-runtime
```

See [kernel provenance and licensing](../../../assets/vm/firecracker/README.md)
when updating or distributing that artifact.

Use private `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` for isolated trials, and keep
host projects outside those directories. The CLI and its companions must stay
available while their VMs run. A development binary built without the verified
init companion can still use an explicit prepared image.

## Linux 7 recipe and qualification

`linux7.py` builds checksum-pinned upstream Linux 7.2.11 with Ubuntu 24.04
userspace and the existing Firecracker/runtime pins. It adapts the pinned 6.18
configuration using `olddefconfig`, saves `config.diff`, and checks required
confinement, filesystem, networking and resource-control options before compiling.
Pressure telemetry is enabled. KVM (`VIRTUALIZATION`, `KVM`, `KVM_INTEL`,
`KVM_AMD`) must be built in, with no module tree, for nested Firecracker in
privileged development boxes; `KVM_INTEL` also selects `X86_FRED`. Nesting
additionally needs host nested virtualization. A reported 2026-10-24 AMD host
run booted, executed in and attached to an inner Firecracker space; inner
teardown, Intel hosts and the full `linux7.py smoke` were not run.
This is the bundled default kernel;
Firecracker 1.17's [supported guest series](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/kernel-policy.md)
does not include Linux 7.
A successful build is not evidence of runtime compatibility.

Maintainer compilation needs Linux amd64, Python 3.11+, GCC/build-essential,
flex, bison, m4, bc, libelf-dev, libssl-dev and xz-utils. Image assembly additionally
needs Go, squashfs-tools, e2fsprogs and dpkg. Use new output directories, preferably
on a native filesystem with several GiB free; only the final image is needed for
the host gate. These are experiment prerequisites, not installed-CLI requirements.

```sh
python3 scripts/vm/firecracker/linux7.py kernel --jobs 2 --output yfc-linux7-kernel
python3 scripts/vm/firecracker/linux7.py image \
  --kernel-build yfc-linux7-kernel --output yfc-linux7-image
make BIN_DIR=bin/linux7 build
# On the Linux amd64 host with /dev/kvm:
python3 scripts/vm/firecracker/linux7.py smoke \
  --image yfc-linux7-image --yoloctl bin/linux7/yoloctl \
  --output /tmp/yolo-linux7-trial
```

The gate uses private config/cache/workdir roots, verifies the candidate and
requires its exact kernel release throughout the normal complete live gate.
Both initial and update image selectors retain Linux 7, including archive import.
The initial guest probe checks the Landlock ABI, cgroup controllers and memory
pressure telemetry, recording `kernel-check.json`. A failure retains the private
fixture for inspection; success destroys the test spaces and retains reports.
Use a different new output path for another run. Never reuse the active coding
space as a test fixture. `bundle-kernel.py` accepts only artifacts matching the
current production recipe. Existing spaces select the new default only through
`space recreate --update-vm-image`; native data/declarations survive, and boxes
require explicit starts.

## Storage and compatibility

Ordinary commands retain the controller, account, OpenShell and session contracts.
Host projects use SSHFS: absolute/escaping symlinks are unsupported. The pinned
SFTP server supports open-handle metadata, requested creation modes and file
fsync; successful namespace mutations sync their pinned parent directories.
These semantics do not provide native cross-kernel locks or database storage.
See [host-share evidence](../../../docs/digests/vm-shares.md). Host port
publication is loopback-only. No systemd runs in this guest.

Writable raw ext4 disks are sparse: the system disk is half of the host's free
space within 30–50 GiB unless `space create --disk` sets it (formatting writes
its inode tables at once, about 610 MiB at 30 GiB);
accounts/declarations use an independent 10 GiB disk. Capacity does not grow
beyond these settings automatically. Writable drives use Firecracker `Writeback`
flush semantics and synchronous discard support. The existing controller's
cancellable storage maintenance trims the two owned ext4 filesystems, allowing
supported host filesystems to reclaim blocks after guest deletion. Unsupported
trim produces a maintenance diagnostic. Trimming never traverses host grants.

BuildKit retention uses the same budgets as Lima, but its daemon starts only for
builds. Background cleanup does not start an idle builder. Recreate discards the
system/container disk and retains native accounts/declarations; destroy removes
owned disks only after verified termination. Public CLI disk resizing remains
unimplemented.

The host filesystems backing both writable disks also need room. Startup checks
for 4 GiB available. The helper measures those filesystems once per second and
pauses the VMM below 2 GiB (or on a measurement error). It resumes automatically
after both have 4 GiB available. `space list` and `space show` expose the pause
and recovery reason. While paused, free host space; ordinary stop/recreate/export
must wait. Do not force-kill the VM: its pending state is still in memory.
No request is automatically retried, so inspect any operation whose reply was
lost. This does not reserve blocks or guarantee protection against rapid
external consumption, quotas or unrelated disk failures. A normal VM stop/start
installs the new helper; a controller-only update does not. No image replacement
is required. See [guard evidence](../../../docs/digests/host-storage.md).

## Image replacement and recovery

Existing spaces keep their selected immutable media. To replace a Firecracker
space with the current build's image and controller:

```sh
yoloctl space recreate fc-trial --update-vm-image
# Or use explicit compatible media:
yoloctl space recreate fc-trial --vm-image /path/to/prepared-image
```

The image is verified before teardown. Accounts/declarations and the native disk
identity survive; sessions, cached container images and ephemeral workdirs do
not. Start boxes explicitly afterward. Other CLI/controller updates do not
replace media. New automatic selections reuse only matching recipe entries.

Interrupted updates retain a bootstrap checkpoint. Resume with
`space recreate fc-trial --update-vm-image` using the same CLI build, or explicitly
recover using `space recreate fc-trial --rollback-vm-image`. The previous image
must be compatible with that build; recovery never rolls native data back.
After completion, another explicit `--vm-image` replacement is needed to select
different media. [Transaction evidence](../../../docs/digests/vm-images.md).

BuildKit's containerd worker uses nerdctl's `default` namespace and
`/var/lib/buildkit/default` cache root. It therefore sees local base images for
composed builds. Boot applies the shared GC policy before lazy builder startup.
[Guest implementation](../../../internal/vmguest/buildkit_linux.go).

## Validation and measurements

The automated gates exercise finite lifecycle, confinement, storage and session
scenarios. A pass does not establish long-duration reliability or coverage of
all host configurations. Provider authentication and native desktop acceptance
retain the limits described in the [smoke guide](../../smoke/README.md).

Run the existing isolated live gate against this adapter:

```sh
SMOKE_BACKEND=firecracker SMOKE_VM_IMAGE="$PWD/yfc-runtime-ready" \
  SMOKE_KEEP_ON_FAILURE=1 bash scripts/smoke/live-vm.sh
```

Its Lima-specific first-boot wrapper injection runs only with Lima. All shared
lifecycle, image, account, policy, attach and operator phases use the selected
adapter. Provider login and native desktop tests retain their documented limits.

Prepare an offline generic-shell fixture to measure startup without downloading
or building a box image during a sample. This is a test image, not an agent image:

```sh
python3 scripts/vm/firecracker/fixture.py \
  --source yfc-runtime-ready/rootfs-tree --output yfc-box-fixture-ready
python3 scripts/vm/firecracker/benchmark.py \
  --image yfc-runtime-ready --fixture yfc-box-fixture-ready/fixture.oci.tar \
  --output yfc-benchmark-ready --samples 5
```

The report separately records initial space creation and subsequent stopped-space
restarts through a confined prompt observed on the normal managed terminal in a
private host PTY. It records controller-to-prompt, stop durations, guest memory,
and separate helper/VMM host RSS. `--readiness exec` selects the older confined
command endpoint for comparison with previous reports. Samples use a cached box
image; image creation/download and provider login are outside that timed restart.
Initial space and box creation durations are reported separately. It reports
whether every measured usable-box sample is within two seconds. A passing
controller-only PoC is not evidence that this full path meets that target.
For a Lima baseline on the same Linux amd64 host, repeat with `--backend lima`,
the same `--fixture`, and a different new output directory; omit `--image`.
Both runs use private configuration and the same stopped-space/box sequence.

`internal/firecracker/TestSoftwareVM` is an opt-in QEMU TCG functional gate with
a separately tagged TCP carrier and a 90-second holder startup allowance for
software emulation. Its output directory must use a filesystem that supports
hole punching: the gate checks host allocation after guest deletion and TRIM on
both writable disks. It builds from `scratch`, then uses that local-only image
as another build's base through the controller. With an offline box fixture,
it also builds a feature-composed box and checks the feature's installed file
through confined execution. Regenerate older fixtures with the current script:
their OCI index must carry the canonical `docker.io/library/yfc-fixture:latest`
reference for Dockerfile `FROM` resolution. It cannot validate Firecracker/KVM timings or the normal
10-second holder deadline. Production builds exclude those test overrides.

Backup or move a space with `space export SPACE BACKUP.tar.gz --stop` and
`space import NEW BACKUP.tar.gz --backend firecracker`. Import creates a fresh
native disk and leaves boxes stopped; `--workdir` and repeatable
`--mount-map OLD=NEW` remap existing host projects. See the CLI help and
[archive contract](../../../docs/digests/space-archives.md) for contents, credentials,
recovery and limits. The combined live gate includes archive round trips.
