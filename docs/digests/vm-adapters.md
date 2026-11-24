# VM adapters

Verification: `4bc09137374e91d374ba6454cd8cef3d3da44240` and accompanying changes (installer consent removal); other facts at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9`; re-read cited sources when changing behavior.

The saved nested virtualization preference is in
[nested virtualization](nested-virtualization.md); the Firecracker helper,
transport, boot wait and disks are in [Firecracker host runtime](firecracker-runtime.md),
its guest, build service and units in [Firecracker guest](firecracker-guest.md).

[FACT] `internal/vmapi.Driver` separates VM lifecycle, owned disks, shell/copy
transport, process termination proof and the fixed controller service from
controller operations. `internal/vm` selects the persisted bootstrap backend.
Empty/legacy selection means Lima; Firecracker is registered only on Linux
amd64. Reads never boot VMs. Host lookup uses only the selected adapter,
rejecting conflicting foreign metadata; guest lookup reports only its own space. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] New Linux amd64 spaces default to Firecracker; other builds default to
Lima. Empty persisted backends remain Lima. Image preparation, verification and cache ownership are documented separately.
[2](#source-2) [4](#source-4)

[FACT] Lima boot preflight resolves the selected home (including existing-home
symlinks), adds `yolo-<space>/ssh.sock.1234567890123456` and requires its byte
length below 104 on Darwin or 108 on Linux, matching Lima's OpenSSH temporary
socket allowance. [6](#source-6) [7](#source-7) [8](#source-8)
Create/import reject before destination publication or VM/disk
effects; start checks before the compatibility offer and again under the
lifecycle lock. Recreate checks before teardown;
deployment and low-level Lima create/start also check. Errors identify the
path, byte count and limit. [9](#source-9) [10](#source-10)
[11](#source-11) [13](#source-13)
This optional adapter check leaves Firecracker,
reads and cleanup unchanged. New-space naming and per-space Lima storage
selection are recorded separately in [Lima storage](lima-storage.md). [12](#source-12)

## Lima stack upgrades

[FACT] Existing Lima spaces adopt the pinned full stack through an explicit
host controller update, space recreation and selected box starts. The stack
digest records the commands, override/drift limits and preserved/lost state;
the host runner separates isolated acceptance from read-only maintenance
observations. [5](#source-5)

## Sources

1. <a id="source-1"></a> [VM boundary](../../internal/vmapi/driver.go#L10) — `Driver`.
2. <a id="source-2"></a> [Adapter selection](../../internal/vm/vm.go#L1) — `Driver`, `ForSpace`, `lookup`, shell and lifecycle routing

3. <a id="source-3"></a> [registration](../../internal/vm/firecracker_linux_amd64.go#L1) .

4. <a id="source-4"></a> [Local VM image preparation](vm-images.md) — recipe, verification and host cache boundaries.

5. <a id="source-5"></a> [Lima stack](lima-stack.md) — explicit upgrade, preservation and QA boundary.

6. <a id="source-6"></a> [Lima preflight](../../internal/lima/socket_paths.go#L21) — `ValidateInstancePaths`, `validateInstanceSocketPath`.
7. <a id="source-7"></a> [Lima v2.2.1 filenames](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/limatype/filenames/filenames.go#L97-L105) — `LongestSock` includes OpenSSH's temporary suffix.
8. <a id="source-8"></a> [Lima v2.2.1 creation](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/instance/create.go#L33-L43) — strict `UnixPathMax` bound.
9. <a id="source-9"></a> [Host lifecycle](../../cmd/yoloctl/controller_lifecycle.go#L71) — `hostControllerLifecycle`, `createControllerSpace`.
10. <a id="source-10"></a> [Import](../../cmd/yoloctl/space_archive.go#L183) — `importSpace` preflight before staging/publication.
11. <a id="source-11"></a> [Deployment](../../cmd/yoloctl/controller_deploy.go#L167) — `startControllerSpaceImpl` before compatibility/disk work.
12. <a id="source-12"></a> [Adapter dispatch](../../internal/vm/vm.go#L83) — optional `ValidateInstancePaths`.
13. <a id="source-13"></a> [Lima entry points](../../internal/lima/lima.go#L41) — `Client.Create`, `Client.Start` before subprocesses.
