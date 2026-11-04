# VM image preparation and replacement

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; OpenShell retirement `1d265df3` and accompanying changes; grow capability `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Firecracker create/import and explicit image updates resolve immutable
media under config-root `vm-images/firecracker`. Automatic selection hashes the
recipe version, guest protocol, linked init SHA256, kernel metadata/config,
package/runtime pins and optional release URL/checksum. A valid matching cache
entry needs no build tools or network. Otherwise native Go assembly prepares it
locally. Explicit `--vm-image DIR` verifies/installs that image without replacing
the recipe default. Existing spaces retain their recorded image path.
Images from recipe 3 record `data_disk_grow: 1`, the pre-mount data disk grow
of their init, and assembly requires `e2fsck` and `resize2fs`; older images
must be updated before a [data disk grow](data-disk-grow.md).
[1](#source-1) [2](#source-2) [25](#source-25)

[FACT] The CLI bundle includes Linux controller companions and a static Linux
amd64 init. Preparation verifies the init ELF and linked SHA256 and unpacks the
embedded checksum-pinned kernel. Normal host builds disable cgo. Image assembly,
sparse disk creation and SSH transport require no host Python, compiler,
filesystem utilities or SSH executable; Lima has separate prerequisites.
Changing image assembly logic without changing its inputs/init requires bumping
`imageRecipeVersion`. [2](#source-2) [3](#source-3) [4](#source-4) [15](#source-15)

[FACT] Ubuntu squashfs, Debian archives and runtime archives are decoded in Go.
Links remain in a virtual tree; guest ELF dependencies are checked before ext4
creation. The image contains init, containerd/nerdctl/BuildKit, OpenShell and
transport tools, with root ownership and sudo's setuid mode, plus a CA store
regenerated from a pinned package. It contains no account/project data,
per-space SSH identity or deployed controller.
[4](#source-4) [5](#source-5) [24](#source-24)

[FACT] Image metadata records schema, architecture, guest protocol and hashes of
kernel, rootfs and Firecracker. Four regular no-follow files are verified and
atomically installed under their manifest identity, preserving sparse regions,
before publishing an automatic default. Reused verification requires matching
manifest and file identities/size/timestamps. Stable cancellable locks serialize
installation and cached downloads; failed preparation cannot select partial
media. Build inputs/logs live under cache-root `vm-build/firecracker`, separate
from installed media. Host grants reject these managed paths.
[1](#source-1) [4](#source-4) [6](#source-6) [7](#source-7)

[FACT] Ordinary starts also require the artifact stamps recorded at VM creation
(inode, size, mode, modification/change times); a changed host device number
alone, as after a reboot or remount renumbers the filesystem, is accepted
(`sameArtifactFile`). Restoring identical bytes under new inodes still
invalidates those stamps. Explicit recreation with
`--vm-image` pointing to the recorded image directory rechecks its checksums
before teardown; mismatched bytes are rejected. [6](#source-6) [8](#source-8)
[11](#source-11)

[FACT] Plain `space recreate` preserves the selected media;
`space start --update-controller` changes only the controller bundle.
`space recreate --update-vm-image` selects this build's image and controller;
`--vm-image DIR` explicitly replaces them using verified media. Both discard the
disposable VM runtime, retaining the existing native disk/UUID and declarations;
boxes need explicit starts. Verification precedes old-VM teardown. A host-locked,
fsynced bootstrap checkpoint records previous/target image, build and phase.
`prepared` retries termination-proof deletion; `selected` resumes boot/deployment
without deleting its replacement VM. [8](#source-8) [9](#source-9)

[FACT] A pending update blocks ordinary host controller commands, mount changes
and ordinary starts. Retry `space recreate --update-vm-image` with the same build.
`--rollback-vm-image` instead recreates using the checkpoint's previous image,
only if compatible with that build. It is not an account/declaration snapshot
rollback. After success there is no pending rollback record; an explicit
`--vm-image` replacement can select other compatible retained media. Interrupted
updates never grant disk-format authority. An inactive/failed controller permits
deployment recovery; an active or unproven controller requires a successful
drain. [8](#source-8) [9](#source-9) [10](#source-10) [16](#source-16)

[FACT] Tests cover changed recipes, cache reuse, cancellation, unsafe archives,
preparation failures, checkpoint publication failures and retry/recovery ordering.
The live lifecycle smoke adds an invalid-image preflight, image replacement and
native-byte/UUID retention. It selects this build's recipe (preparing it if absent)
unless `SMOKE_UPDATE_VM_IMAGE` supplies an explicit compatible bundle. Injected failures
are not VM crash testing. Packaging supports explicit four-member tar.gz bundles;
there is no unified OCI resolver/publisher. [11](#source-11) [12](#source-12)
[13](#source-13) [14](#source-14) [17](#source-17)

[FACT] The bundled kernel is upstream Linux 7.2.11, checksum-pinned with its
complete configuration and compiler provenance. The maintainer recipe adapts
Firecracker's 6.18 configuration with `olddefconfig`, enables pressure telemetry,
checks confinement/resource requirements and fixes build metadata. It requires
built-in `VIRTUALIZATION`, `KVM`, `KVM_INTEL` and `KVM_AMD`, so nested KVM needs
no guest module tree; `KVM_INTEL` selects `X86_FRED`. Qualification
retains the selected kernel across image replacement/import and probes Landlock,
cgroups and pressure telemetry. Recipe checks alone do not prove live compatibility.
leogr reported nested Firecracker booting on this kernel on an AMD host; inner
teardown and Intel hosts are unverified.
[18](#source-18) [19](#source-19) [20](#source-20) [21](#source-21) [22](#source-22) [23](#source-23)

[FACT] The OpenShell upgrade removes the `openshell.tgz` input and the retired
`/opt/openshell` paths from newly assembled Firecracker images only and bumps
`imageRecipeVersion` from 3 to 4; existing images, VM files and data are not
rewritten. Lima provisioning no longer downloads `/opt/openshell` or the old
shared policy file. [26](#source-26)

[FACT] Before draining or replacing a Firecracker controller, `space start`
reads the running init's OpenShell capability (a stopped VM is booted to read
it, without updating media or touching boxes); an old image is refused with
the exact `space recreate NAME --update-vm-image` instruction, while a
transport failure stays "unavailable", never a false "old" verdict. Image
replacement remains that explicit operation. [27](#source-27)

[FACT] Lima provisioning now installs the distribution's nftables package with
its own dependencies (not a pinned version), readiness requires the `nft`
executable, and provisioning leaves a version-2 system-provision marker (a
broken version-1 marker is rejected); host commands that would mutate runtime
check that marker from the local Lima configuration before preparing the VM,
controller or box and before sending any request. A
typed mismatch offers an explicit yes/no VM recreation in an interactive
session (the approved host lifecycle recreates, then the unsent operation runs
once); scripts receive the exact `space recreate NAME` instruction and no
mutation. An explicit Lima recreate selects the current build, so no
`--update-controller` loop follows. [28](#source-28)

## Sources

1. <a id="source-1"></a> [Installer](../../internal/firecracker/images_linux.go#L33) — `ResolveImage`, `resolveImage`, `copyImageFile`, `copySparse`.
2. <a id="source-2"></a> [Recipe identity](../../internal/firecracker/recipe_linux.go#L21) — `defaultRecipe`, inputs/version.
3. <a id="source-3"></a> [Init verification](../../internal/guestbinary/binary.go#L24) — `ResolveInit`.
4. <a id="source-4"></a> [Preparation](../../internal/firecracker/prepare_linux.go#L21) — `imageLock`, `prepareLocalImage`.
5. <a id="source-5"></a> [Native assembly](../../internal/firecracker/assemble_linux.go#L607) — `assembleLocalImage`: virtual tree, ELF checks and ext4.
6. <a id="source-6"></a> [Media verification](../../internal/firecracker/config.go#L121) — `VerifyImage`, `unchangedImage`.
7. <a id="source-7"></a> [Grant checks](../../cmd/yoloctl/space_lifecycle.go#L21) — `rejectManagedHostPath`, `rejectSpaceHostPath`.
8. <a id="source-8"></a> [Image transaction](../../cmd/yoloctl/space_image_update.go#L19) — `recreateSpaceImage`, `replaceSpaceImage`.
9. <a id="source-9"></a> [Lifecycle routing](../../cmd/yoloctl/controller_lifecycle.go#L25) — flags, host lock, dispatch.
10. <a id="source-10"></a> [Checkpoint](../../internal/spaceconfig/bootstrap.go#L44) — `VMImageUpdate`, `Validate`, `Save`.
11. <a id="source-11"></a> [Installer tests](../../internal/firecracker/images_linux_test.go#L44) — lazy install, failure/retry, cancellation, restored-file verification, identity, archives.
12. <a id="source-12"></a> [Update tests](../../cmd/yoloctl/space_image_update_test.go#L26) — publication/teardown/boot boundaries, recovery and admission.
13. <a id="source-13"></a> [Lifecycle smoke](../../scripts/smoke/space-lifecycle.sh#L101) — rejected image, replacement and retained native bytes.
14. <a id="source-14"></a> [Packaging](../../scripts/vm/firecracker/package.py#L1) — `package`.
15. <a id="source-15"></a> [Coordinated build](../../Makefile#L42) — host, Linux companions, init hash linkage and cgo settings.
16. <a id="source-16"></a> [Deployment admission](../../cmd/yoloctl/controller_deploy.go#L83) — pending-image gate, drain/state proof and start.
17. <a id="source-17"></a> [Recipe tests](../../internal/firecracker/recipe_linux_test.go#L15) — all inputs, default isolation and retained media.
18. <a id="source-18"></a> [Linux 7 recipe and gate](../../scripts/vm/firecracker/linux7.py#L57) — `kernel`, `verify_image`, `smoke`.
19. <a id="source-19"></a> [Runtime probe](../../scripts/smoke/linux7.sh#L23) — Landlock, controllers and telemetry before the full gate.
20. <a id="source-20"></a> [Per-phase kernel check](../../scripts/smoke/common.sh#L135) — `smoke_ensure_space`.
21. <a id="source-21"></a> [Bundled recipe](../../assets/vm/firecracker/inputs.py#L24-L44) — `REQUIRED`, `KERNEL_EXTRAS`; `kernel` at L163.
22. <a id="source-22"></a> [Bundled provenance](../../assets/vm/firecracker/kernel.json#L1) — source, configuration and compiler identities.
23. <a id="source-23"></a> [Effective configuration](../../assets/vm/firecracker/kernel.config#L711-L718) — built-in KVM; `X86_FRED` at L364.
24. <a id="source-24"></a> [CA trust stores](trust-stores.md) — package pin and store regeneration.
25. <a id="source-25"></a> [Grow capability](../../internal/firecracker/config.go#L28-L43) — `Image.DataDiskGrow`, `DataDiskGrowVersion`; `verifyRuntime` in assemble_linux.go; `imageRecipeVersion` in recipe_linux.go.
26. <a id="source-26"></a> [Retired inputs](../../internal/firecracker/recipe_linux.go#L21) — `imageRecipeVersion` 4; retired paths in assemble_linux.go; `assets/vm/firecracker/pins.json` without `openshell.tgz`; Lima template without the download/Rego sections (retirement tree `3cc5e841`).
27. <a id="source-27"></a> [cmd/yoloctl](../../cmd/yoloctl/controller_deploy.go#L1) — `checkSpaceOpenShellImage` in `space_openshell_image.go`, called from `controller_deploy.go` (sol2 space-old-image-refusal tree `f9260ea0`).
28. <a id="source-28"></a> [cmd/yoloctl](../../cmd/yoloctl/controller_deploy.go#L1) — `space_vm_compatibility.go`, `internal/lima/openshell_compatibility.go`, Lima template marker (sol lima-vm-preflight tree `84a754b7`; nftables package and marker v2 from lima-nft-package tree `f018d8b8`).
