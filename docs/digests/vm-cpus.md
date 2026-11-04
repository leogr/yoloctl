# Automatic VM CPUs

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] Host `space create` accepts `--cpus auto` (default) or a positive
integer. Automatic selection uses every logical CPU in the process affinity
mask (which reflects cpuset placement), narrowed by the tightest CFS bandwidth
visible along the process's own cgroup v2 and legacy v1 CPU hierarchies. A
fractional bandwidth rounds up, minimum one vCPU, without adding CPU time;
Firecracker clips the result to 32 and Lima has no fixed maximum. Selection
and adapter validation happen before image preparation or disk effects; the
chosen count and its basis are printed, then the concrete integer is persisted
in the bootstrap. Explicit counts never measure the host and keep
adapter validation. Starts, recreates and imports keep saved counts. It uses no
GOMAXPROCS, load sampling or other-VM subtraction. [1](#source-1)
[2](#source-2) [3](#source-3) [6](#source-6) [8](#source-8)

[FACT] The Linux reader resolves membership from `/proc/self/cgroup` and mount
roots from `/proc/self/mountinfo`, picks the broadest mount root containing
that membership (so a subtree bind cannot hide visible ancestors), then walks
from the mount point with descriptor-relative, no-follow opens that must stay
on the mount's device and whose mount must have the cgroup filesystem type. A level without the
controller's files has no limit; unlimited values are ignored; unreadable or
malformed limit files, oversized reads, or membership outside a mounted
hierarchy fail automatic selection with an explicit `--cpus` remedy. An
unmounted hierarchy is not visible; a cgroup namespace may hide ancestor limits,
so the result is caller-visible, not a guaranteed entitlement. macOS reads
`hw.logicalcpu` with no bandwidth claim; other hosts fail. [2](#source-2)
[4](#source-4) [5](#source-5) [7](#source-7)

[FACT] Host `space set` and `space edit` keep positive-integer CPU counts;
`auto` is creation-only. Focused tests cover selection, refusal before effects,
import retention and cgroup fixtures; they do not validate a live VM's vCPU
count or a host's cgroup layout. [3](#source-3) [5](#source-5) [8](#source-8)
[9](#source-9)

## Sources

1. <a id="source-1"></a> [cmd/yoloctl/space_cpu.go](../../cmd/yoloctl/space_cpu.go#L24) — `resolveSpaceCPUs`: explicit parse, measurement, clipping and basis message.
2. <a id="source-2"></a> [internal/resources/cpu.go](../../internal/resources/cpu.go#L28) — `VMCPUs` rounding/clipping, membership, mountinfo and limit decoders.
3. <a id="source-3"></a> [cmd/yoloctl/space_cpu_test.go](../../cmd/yoloctl/space_cpu_test.go#L31) — default, completion, measured/explicit selection and refusal before effects.
4. <a id="source-4"></a> [internal/resources/cpu_linux.go](../../internal/resources/cpu_linux.go#L20) — `ReadHostCPU`, `walkCgroupBandwidth` and filesystem checks.
5. <a id="source-5"></a> [CPU regressions](../../internal/resources/cpu_linux_test.go#L28) — ancestor/leaf/unlimited/v1 and subtree-alias fixtures, symlink, oversize and escape refusals.
6. <a id="source-6"></a> [cmd/yoloctl/controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L216) — `createControllerSpace` selects and validates before image/disk effects.
7. <a id="source-7"></a> [internal/resources/cpu_darwin.go](../../internal/resources/cpu_darwin.go#L14) — macOS `hw.logicalcpu`.
8. <a id="source-8"></a> [cmd/yoloctl/space_archive_test.go](../../cmd/yoloctl/space_archive_test.go#L54) — import keeps archived CPUs without measuring.
9. <a id="source-9"></a> [cmd/yoloctl/space_resources_test.go](../../cmd/yoloctl/space_resources_test.go#L99) — `space set --cpus auto` is refused with other invalid values.
