# Nested virtualization

Verification: `cfdd42c9025f5683bc415388d249e3c996afceb8` and accompanying changes; resolution and the Lima resource update on `fc48bca38d7777c07e13bb1610611f9a15c8c17d` and accompanying changes.

[FACT] The saved per-space `nested_virtualization` preference is absent/`auto`,
`on` or `off`. Absent means auto without rewriting older bootstraps. Create and
stopped-space set expose `--nested-virtualization`; recreate and archives retain it.
Before boot, macOS Lima writes an explicit boolean through the existing
stopped-instance/no-follow resource update, including newly created instances.
A stopped initialization/import checkpoint permits an off-only remedy after a
rejected first boot: only the preference changes; no resources, checkpoints or
instance file are changed and no boot is retried. Image recovery and mixed resource
changes remain refused. Deterministically unsupported explicit-on creates fail
before allocation. The creation template and confinement policy are unchanged. [1](#source-1)
[2](#source-2)

[FACT] Auto enables only a listed `vz` instance with an `aarch64` guest and
host facts matching Apple M3+ and macOS 15+. This is a conservative heuristic,
not Apple's capability query. Explicit on overrides host eligibility but refuses
other driver/guest routes, non-macOS and Firecracker. Auto/off do not manage this
key on non-macOS or Firecracker; off writes false on macOS Lima.
Lima emits a notice when auto leaves nested virtualization disabled by yoloctl.
Explicit off/on and already-running starts are silent. For Lima, `space show`
reports the saved preference, instance-file value and current host eligibility
without inferring effective guest KVM. [1](#source-1) [3](#source-3)

[FACT] Firecracker skips the boot notice. `space show` reports that nested KVM
depends on host KVM support and the guest kernel. The bundled guest kernel
includes built-in KVM support for Intel and AMD. [3](#source-3) [4](#source-4)

## Sources

1. <a id="source-1"></a> [Nested resolution](../../internal/vmapi/nested_virtualization.go#L1) — `ResolveNestedVirtualization`, `NestedHostFacts.Eligible`.
2. <a id="source-2"></a> [Lima resource update](../../internal/lima/resources.go#L1) — `SetInstanceResources`, `setResourcesInYAML` guarded explicit boolean update.
3. <a id="source-3"></a> [Host boot and display](../../cmd/yoloctl/space_nested_virtualization.go#L19-L86) — `applySpaceBootResources`, `fillNestedSpaceShow` and `nestedBootNotice`.
4. <a id="source-4"></a> [Bundled guest KVM config](../../assets/vm/firecracker/kernel.config#L718-L725) — built-in virtualization, KVM, KVM_INTEL and KVM_AMD.
