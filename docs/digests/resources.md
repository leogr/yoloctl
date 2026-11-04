# VM resource settings

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; disk field `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] Host `space create` accepts `--memory auto` (default) or a positive
integer size in KiB, MiB or GiB. Automatic selection happens before image
preparation and persists a concrete allocation in the bootstrap; starting or
recreating that space uses the saved value. Import retains the archived VM
settings. An explicit size bypasses automatic measurement and its 4–16 GiB
range, subject to the selected adapter's constraints. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] The target is one quarter of host RAM, clamped to 4–16 GiB. The host
reserve is the larger of 2 GiB and one eighth of RAM. The allocation must fit
both available memory minus that reserve and total memory minus the reserve
and configured RAM of other live yoloctl VMs. The result rounds down to whole
GiB; insufficient room for 4 GiB is an error with an explicit-override hint.
Paused and uncertain VM states count as allocated; stopped VMs do not. Only
adapters represented by existing space declarations are consulted, once each,
under a ten-second measurement budget. [1](#source-1) [4](#source-4)
[5](#source-5)

[FACT] Linux reads `MemTotal` and `MemAvailable`. macOS reads `hw.memsize` and
the free, inactive and speculative page counts printed by a bounded `vm_stat`
command. That command prints free pages with speculative pages subtracted;
adding speculative pages once restores them. Purgeable and compressed pages
are not added separately. Invalid or missing counters fail automatic
selection. macOS available memory is an estimate, not Linux's MemAvailable
calculation. [4](#source-4) [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] Selection takes one measurement and saves a boot setting. It does not
reserve host memory, balloon existing VMs or change saved box memory limits.
[1](#source-1) [4](#source-4)

[FACT] New-space CPU selection is described in
[automatic VM CPUs](vm-cpus.md). [19](#source-19)

[FACT] Host `space set SPACE` accepts optional `--memory auto|SIZE` and
`--cpus N`; omitted fields retain saved values. `space edit SPACE` opens private
flat YAML containing only `cpus`, `memory` and `disk`; failed saves retain the edited file.
`--disk` and `disk:` grow the saved system disk for the next recreate (see
[VM system disk size](vm-disk-size.md)).
They require an existing verified stopped VM, reject
pending initialization/import/image updates, and never boot or stop it.
The host lifecycle lock spans validation and publication; editor waits release
it and saving rechecks bootstrap identity/content and stopped state.
`auto` uses the existing RAM policy; CPU changes remain explicit integers.
[11](#source-11) [1](#source-1)

[FACT] Validated desired resources persist in the host bootstrap before the
backend update. An apply error retains desired settings and reports failure;
the next explicit start synchronizes them before booting an existing stopped
VM. A repeated set also repairs backend drift. No runtime, disk, grant, image,
account or box limit is replaced by this operation. Import still preserves
archived resources: `space import NEW ARCHIVE --no-start`, then set resources
and explicitly start NEW. Import first boots to restore, then stops.
[11](#source-11) [12](#source-12) [3](#source-3)

[FACT] Backends independently refuse live/paused/uncertain VMs. Firecracker
updates only saved `spec.cpus`/`spec.memory` under its instance lock; CPUs must
be 1–32 and RAM at least 128 MiB in whole MiB. Lima changes only the resolved
YAML CPU/memory fields, retaining other keys/comments, with positive CPUs and
parseable positive RAM. Both preserve existing storage/identity and publish
atomically; equal backend values need no rewrite. [13](#source-13)
[14](#source-14) [15](#source-15)

[FACT] Unit tests cover capacity/headroom limits, other VM allocations, paused
states, malformed counters, Darwin page sizes, explicit overrides and an empty
inventory without installed hypervisors. Isolated VM smokes request 4 GiB and
4 CPUs explicitly by default (`SMOKE_VM_MEMORY`/`SMOKE_VM_CPUS` override them),
keeping their resource budget independent of host RAM and CPU count. [5](#source-5) [9](#source-9)
[10](#source-10)

[FACT] Focused resource regressions use temporary configurations and fake VM
state to check narrow writes, no-op preservation, refusal, editor conflicts
and retained desired settings after apply failure. They do not establish live
Firecracker/Lima resizing or restart behavior. [16](#source-16) [17](#source-17)
[18](#source-18)

## Sources

1. <a id="source-1"></a> [cmd/yoloctl/space_memory.go](../../cmd/yoloctl/space_memory.go#L18) — `selectSpaceMemory`, `resolveSpaceMemory`, bounded inventory and explicit override.
2. <a id="source-2"></a> [cmd/yoloctl/controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L166) — `createControllerSpace` selection and persisted bootstrap.
3. <a id="source-3"></a> [cmd/yoloctl/space_archive.go](../../cmd/yoloctl/space_archive.go#L260) — `prepareImportedSpace` retains VM settings; import `--no-start` stops after restore.
4. <a id="source-4"></a> [internal/resources/memory.go](../../internal/resources/memory.go#L22) — sizing policy, units and measurement decoders.
5. <a id="source-5"></a> [cmd/yoloctl/space_memory_test.go](../../cmd/yoloctl/space_memory_test.go#L11) — live-state accounting and override tests.
6. <a id="source-6"></a> [internal/resources/host_linux.go](../../internal/resources/host_linux.go#L10) — Linux memory measurement.
7. <a id="source-7"></a> [internal/resources/host_darwin.go](../../internal/resources/host_darwin.go#L13) — macOS memory measurement.
8. <a id="source-8"></a> [Apple vm_stat at 2085a5d15f370b5580365275555afd313e845550](https://github.com/apple-opensource/system_cmds/blob/2085a5d15f370b5580365275555afd313e845550/vm_stat.tproj/vm_stat.c#L125) — printed page categories.
9. <a id="source-9"></a> [scripts/smoke/common.sh](../../scripts/smoke/common.sh#L143) — smoke_create_space explicit memory/CPU budget.
10. <a id="source-10"></a> [internal/resources/memory_test.go](../../internal/resources/memory_test.go#L8) — policy, decoder and explicit-unit tests.
11. <a id="source-11"></a> [cmd/yoloctl/space_resources.go](../../cmd/yoloctl/space_resources.go#L43) — `newSpaceSetCmd`, `newSpaceEditCmd`, stopped guard, snapshots, editor and desired-first publication.
12. <a id="source-12"></a> [cmd/yoloctl/controller_deploy.go](../../cmd/yoloctl/controller_deploy.go#L151) — `startControllerSpaceImpl` synchronizes saved resources before an existing stopped VM starts.
13. <a id="source-13"></a> [internal/vmapi/resources.go](../../internal/vmapi/resources.go#L23) — `ValidateResources` adapter bounds and stopped-state error.
14. <a id="source-14"></a> [internal/firecracker/driver_linux.go](../../internal/firecracker/driver_linux.go#L168) — `SetResources` stopped/identity checks and narrow configuration update.
15. <a id="source-15"></a> [internal/lima/resources.go](../../internal/lima/resources.go#L21) — `SetInstanceResources`, stopped/process checks and resolved-YAML resource updates.
16. <a id="source-16"></a> [cmd/yoloctl/space_resources_test.go](../../cmd/yoloctl/space_resources_test.go#L73) — preservation, refusal, pending apply and editor/host-only regressions.
17. <a id="source-17"></a> [Firecracker regressions](../../internal/firecracker/resources_linux_test.go#L57) — narrow writes, stopped-state proof and temporary configurations.
18. <a id="source-18"></a> [Lima regressions](../../internal/lima/resources_test.go#L46) — narrow writes, stopped-state proof and temporary configurations.
19. <a id="source-19"></a> [Automatic VM CPUs](vm-cpus.md) — creation-time CPU selection digest.
