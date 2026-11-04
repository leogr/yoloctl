# Declarations, ownership and workdirs

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] A space owns a selected-adapter VM and an independent ext4 data disk. Host
`spaces/<space>/space.yaml` is schema-3 bootstrap only: identity, disk ID/UUID,
initialization, developer UID/GID, host grants, VM boot settings, selected bundle
and generation, with a temporary checkpoint during explicit VM-image replacement.
Native `config/space.yaml` is schema-1 settings: name, default
account and default workdir. VM workers refuse host bootstrap reads, so those
settings are never decoded as a bootstrap. Native `config/boxes` and `config/accounts` hold
entity declarations. Normal clients never fall back to host entity files.
Schema-2 records are rejected with a previous-build recovery hint. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] Layout helpers scope VM workers to the verified native store and native
lock paths. Global catalogs/context/TUI preferences remain host-owned; only
three catalog directories are mounted read-only into the VM, then snapshotted
per operation. The Lima adapter delegates its hypervisor, architecture
and mount transport to Lima, preserving Linux/macOS/Rosetta selection. The
bootstrap persists the selected backend. New Linux amd64 spaces default to
Firecracker; other platforms and empty legacy records select Lima. [2](#source-2)
[4](#source-4) [5](#source-5) [20](#source-20) [21](#source-21)

[FACT] The Lima template pins official Ubuntu Minimal 24.04 release-20261001 images
and SHA256 digests for x86_64/aarch64. Provisioning runs apt only when a required
package lacks dpkg's installed status or `ca-certificates` predates Ubuntu's
trust fix ([CA trust stores](trust-stores.md)). It no longer downloads or
provisions OpenShell: the native runtime executables, helper and Rego are
embedded in yoloctl and installed per box, and the VM-side `/opt/openshell`
installation is retired. [22](#source-22)
[23](#source-23) [26](#source-26) Script tests simulate package/network commands
and failures, not real VM boots or performance. [24](#source-24) [25](#source-25)

[FACT] Lima also selects SHA256-pinned nerdctl-full 2.3.5 for both guest
architectures. The stack digest records empty-archive drift, overrides and
mixed-stack privilege-evidence limits. [27](#source-27)

[FACT] Box desired state includes agent/account, base image, workdir, features,
capabilities, literal environment, ports, mounts, resources, network mode,
extra agent arguments and agentless session argv. Real agents require an existing
account; agentless boxes use `none`. Creating a space initializes its required first account; later
account creation is explicit. Same-name accounts across spaces are independent.
[3](#source-3) [6](#source-6)

[FACT] Saving a box or resolving its runtime image requires a nonempty, parseable
image reference. Invalid references report the box name. Loading remains
permissive so invalid existing declarations can be inspected and repaired;
storage cleanup fails closed until their image references are corrected.
[17](#source-17) [18](#source-18) [19](#source-19)

[FACT] Explicit space flags/positionals precede the context pin and `default`.
Host selection happens before dispatch/default-account lookup; operator callers
are fixed to their own space. Host paths expand on the caller; operator-relative
paths translate through actual container binds. [7](#source-7) [8](#source-8)

[FACT] Host workdirs canonicalize existing ancestors, including symlinks, even
when the final path does not exist. Container destinations mirror the canonical
host path under `/workdir`. Ephemeral workdirs are disposable VM-local storage
at `/workdir`. Ordinary binds need a compatible most-specific host grant and a
live shared directory; box commands never widen grants. Managed account binds
are derived separately. Host grant/bind checks cover intersections with config, the host VM build cache,
account disk aliases and controller infrastructure. [9](#source-9)
[10](#source-10) [11](#source-11)

[FACT] Host RPC path expansion checks canonical mount/workdir arguments against
the same managed roots as host space grants, using the selected bootstrap's
disk identity. This rejects host aliases such as macOS `/private/var` before
forwarding, including declaration-only create/copy arguments.
The worker independently checks its native paths and live grants; ordinary
ungranted project declarations may still defer grant checks. [10](#source-10)
[11](#source-11) [14](#source-14) [15](#source-15) [16](#source-16)

[FACT] `box copy` copies settings, not project contents, account state or runtime.
Cross-space copy reads a source snapshot and selects the destination's account
of that name unless overridden. Ephemeral storage is derived for the new box.
Both controllers must be running; no distributed write transaction is needed.
[8](#source-8) [12](#source-12)

[FACT] Agent profiles describe launch/auth/state conventions, image defaults and
required features. Built-ins take precedence over custom names. They describe
adapters, not identities. Optional `state_archive_excludes` lists literal
state-relative paths/subtrees omitted by export; roots, traversal and globs are
rejected. This does not delete native state or change credential import.
[13](#source-13)

## Sources

1. <a id="source-1"></a> [internal/spaceconfig/bootstrap.go](../../internal/spaceconfig/bootstrap.go#L24) — Bootstrap, Settings, strict schema parsing and `LoadBootstrap`.
2. <a id="source-2"></a> [internal/layout/layout.go](../../internal/layout/layout.go#L95) — native/host path construction.
3. <a id="source-3"></a> [cmd/yoloctl/controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L173) — initial account and private initialization seed.
4. <a id="source-4"></a> [cmd/yoloctl/controller_deploy.go](../../cmd/yoloctl/controller_deploy.go#L25) — host grants and narrow read-only catalog mounts.
5. <a id="source-5"></a> [internal/lima/template.go](../../internal/lima/template.go#L31) — template boot settings, Lima-selected backend/architecture.
6. <a id="source-6"></a> [internal/agentbox/agentbox.go](../../internal/agentbox/agentbox.go#L54) — box schema and validation.
7. <a id="source-7"></a> [cmd/yoloctl/context.go](../../cmd/yoloctl/context.go#L14) — shared space selection.
8. <a id="source-8"></a> [cmd/yoloctl/controller_client.go](../../cmd/yoloctl/controller_client.go#L40) — host/operator target and path selection, copy snapshots.
9. <a id="source-9"></a> [internal/agentbox/workdir.go](../../internal/agentbox/workdir.go#L26) — workdir identity and canonicalization.
10. <a id="source-10"></a> [cmd/yoloctl/space.go](../../cmd/yoloctl/space.go#L725) — grant classification and live bind checks.
11. <a id="source-11"></a> [cmd/yoloctl/space_lifecycle.go](../../cmd/yoloctl/space_lifecycle.go#L20) — protected root rejection.
12. <a id="source-12"></a> [cmd/yoloctl/agentbox_copy.go](../../cmd/yoloctl/agentbox_copy.go#L24) — copy merge and destination validation.
13. <a id="source-13"></a> [internal/agentprofile/profile.go](../../internal/agentprofile/profile.go#L52) — adapter schema and lookup.
14. <a id="source-14"></a> [cmd/yoloctl/controller_client.go](../../cmd/yoloctl/controller_client.go#L201) — `expandControllerPaths` validates and canonicalizes host path flags.
15. <a id="source-15"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L1771) — `rejectAccountRootBinds` checks the native worker's path namespace.
16. <a id="source-16"></a> [cmd/yoloctl/controller_client_test.go](../../cmd/yoloctl/controller_client_test.go#L18) — host alias, disk identity, project-path and pre-dispatch command regressions.
17. <a id="source-17"></a> [resolved_image.go](../../internal/agentbox/resolved_image.go#L12) — image validation at resolution.
18. <a id="source-18"></a> [agentbox_reconcile_test.go](../../cmd/yoloctl/agentbox_reconcile_test.go#L101) — reject invalid changes; repair a missing image.
19. <a id="source-19"></a> [agentbox.go](../../internal/agentbox/agentbox.go#L549) — image validation at persistence.
20. <a id="source-20"></a> [internal/vm/vm.go](../../internal/vm/vm.go#L1) — `Driver`, `ForSpace`
21. <a id="source-21"></a> [bootstrap](../../internal/spaceconfig/bootstrap.go#L58) — `VMSettings`, `Validate`.
22. <a id="source-22"></a> [assets/lima/yoloctl.yaml.tmpl](../../assets/lima/yoloctl.yaml.tmpl#L6) — image pins and system provisioning.
23. <a id="source-23"></a> [assets/embed.go](../../assets/embed.go#L32-L35) — `SandboxVersion` v0.1.6; executable/Rego bytes come from the compiled qualification and embedded artifacts, not a VM-side download.
24. <a id="source-24"></a> [internal/lima/template_test.go](../../internal/lima/template_test.go#L67) — `TestTemplatePinsMinimalImagesForBothGuestArchitectures`.
25. <a id="source-25"></a> [internal/lima/provision_test.go](../../internal/lima/provision_test.go#L59) — `TestVMRuntimePackageProvisioning`, `TestCACertificatesProvisioningUpgrade` at L129 and `TestOpenShellProvisioningVerificationAndRecovery`, package reuse, CA upgrade, corruption and retry coverage.
26. <a id="source-26"></a> [Canonical release checksums](https://cloud-images.ubuntu.com/minimal/releases/noble/release-20261001/SHA256SUMS) — pinned amd64/arm64 Minimal images.
27. <a id="source-27"></a> [Lima stack](lima-stack.md) — archive selection and privilege evidence.
