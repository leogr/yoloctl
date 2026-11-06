# Model and ownership

[FACT] A space is the VM boundary with host-owned boot authority and an
independent ext4 data disk. The VM controller owns defaults, box/account
manifests and secrets on that disk. Host projects remain explicit shares;
ephemeral workdirs and runtime containers are disposable system-disk data.
[1](#source-1) [2](#source-2)

[FACT] New spaces have a 16-character name cap across backends; existing
longer identities remain valid. New macOS Lima spaces record their selected
`~/.config/yoloctl/lima` home; existing records without that field retain the
legacy home. VM keys and disks stay in their selected location, independently
of host bootstrap/catalog storage. Linux placement is unchanged.
[17](#source-17)

[FACT] New spaces choose VM RAM once from host capacity and available memory,
within 4–16 GiB, while accounting for other live space allocations. `--memory`
overrides that choice. The saved allocation survives starts/recreates; this
heuristic does not reserve host RAM or change existing box limits. VM CPUs are
likewise chosen once from the caller-visible logical CPUs and cgroup bandwidth,
within adapter bounds, unless `--cpus N` is given. The system disk is half of
the host filesystem's free space within 30–50 GiB unless `--disk SIZE` is
given; it is sparse, and Firecracker refuses a host that cannot format it
above the start headroom. The data disk is 10 GiB unless `--data-disk` is
given and only grows offline [15](#source-15). [7](#source-7) [13](#source-13) [14](#source-14)

[FACT] Host `space set` and `space edit` change only a verified stopped space's
VM RAM/CPU settings and its saved system disk size. Memory can be explicitly sized or recomputed with `auto`;
CPU changes are explicit counts. Changes retain the VM/data/container disks and identities,
take effect at the next explicit start and do not restart workloads. The disk
size only grows and applies at the next `space recreate`, which replaces the
system disk anyway [14](#source-14). `space set` also saves the image layer
compression the controller receives at the next start
([image preparation](../digests/image-preparation.md)). Import
retains archived resources; import with `--no-start`, then set resources before
starting the restored space. [7](#source-7)

[FACT] New boxes share an aggregate guest memory pool with BuildKit and its
build steps; explicit per-box caps remain available. The pool leaves headroom
for VM infrastructure and reports usage/peak/OOM counters in `box inspect`.
New boxes also have no CPU cap unless `--cpus` sets one.
Existing containers join through recreation; an old Firecracker guest requires
explicit image replacement first. This is not a per-box RAM reservation or a
guarantee that every workload fits concurrently. [8](#source-8)

[DERIVED] Concurrent allocation, other applications and later VM starts can
consume measured headroom; automatic sizing cannot guarantee against host OOM.
[7](#source-7)

[FACT] Boxes declare desired execution, policy and account selection. Accounts
are space-local multi-agent identities; real agents require existing accounts,
agentless boxes use `none`. `space create --account` initializes the first
identity. Copy transfers settings and selects the destination's account; it
never copies account state or project files. [1](#source-1)

[FACT] Agents describe launch/auth/state conventions and image defaults.
Features install tools and require positive capabilities. Catalogs stay on the
host and are read-only inputs to the controller. The persisted VM adapter
selects transport and lifecycle. New Linux amd64 spaces default to Firecracker
with KVM and a lazily prepared local image. Firecracker image assembly and
transport require no installed host build or SSH tools. Other platforms default to Lima,
preserving its Linux/macOS/Rosetta selection. Existing empty backend records
continue to select Lima. [1](#source-1) [3](#source-3) [6](#source-6)

[FACT] New/recreated Lima VMs use pinned Ubuntu Minimal 24.04 images and
nerdctl-full 2.3.5 archives for both guest architectures. Existing unpinned VMs
need the explicit host stack-upgrade route. [6](#source-6) Provisioning reuses
installed dependencies and verified OpenShell binaries, but upgrades an older CA
package through apt. This does not establish a measured startup or memory
improvement. [1](#source-1) [12](#source-12)

[FACT] Updated Firecracker hosts and guest images negotiate framed TCP forwarding
to preserve replies after either endpoint closes its sending side. Older images
retain raw forwarding and its half-close limitation. A controller-only update
does not update the guest bridge. [9](#source-9)

[DERIVED] For ownership changes, identify boot authority, native declarations
and runtime effects separately. A box grant cannot widen the space's host shares
or write its bootstrap; operator powers still execute inside that space.
[1](#source-1) [2](#source-2) [4](#source-4)

[FACT] Host mount/workdir arguments must pass managed-storage checks before
controller forwarding, including declaration-only operations. Canonical host
aliases are checked on the host; the controller retains native-path and live
grant validation. [1](#source-1)

[FACT] Agentless boxes can declare non-secret `session_command` argv for their
managed session. A manual AIRC server box can run `aircd` this way, retaining the
normal box/account and confined launch model. Features install tools and a
collaboration skill without granting endpoint access. [5](#source-5)

[FACT] Managed chat desired state is native controller data. It uses a separate
room identity and per-member authentication, independent of shared accounts.
Box declaration IDs are minted only on chat enrollment and are meaningful
together with the native disk ID. Ordinary legacy saves do not introduce identity
fields. Explicit runtime opt-in projects only the server's private leaves and
each participant's own credential plus narrow Unix endpoint directory.
[10](#source-10) [11](#source-11)

[FACT] Host config/cache roots default to `yoloctl` under the XDG/OS bases for
both release and development builds. One absolute,
clean `YOLOCTL_HOME` selects config and its `cache` child for either build.
Native VM paths retain precedence. Aliases and explicit shared homes are not
independent ownership boundaries. [16](#source-16)

## Sources

1. <a id="source-1"></a> [Declaration and workdir evidence](../digests/declarations.md).
2. <a id="source-2"></a> [Native controller ownership](../digests/controller.md).
3. <a id="source-3"></a> [Features and policy](../digests/policy-images.md).
4. <a id="source-4"></a> [Operator authority](../digests/operator.md).
5. <a id="source-5"></a> [AIRC and agentless sessions](../digests/airc.md).
6. <a id="source-6"></a> [VM adapters](../digests/vm-adapters.md).
7. <a id="source-7"></a> [VM memory selection and stopped-space resources](../digests/resources.md).
8. <a id="source-8"></a> [Workload memory budgets and diagnostics](../digests/workload-memory.md).
9. <a id="source-9"></a> [Port forwarding limits](../digests/vm-ports.md).
10. <a id="source-10"></a> [Managed chat native ownership](../digests/managed-chat.md).
11. <a id="source-11"></a> [Managed chat runtime](../digests/chat-runtime.md).
12. <a id="source-12"></a> [CA trust stores](../digests/trust-stores.md).
13. <a id="source-13"></a> [Automatic VM CPUs](../digests/vm-cpus.md).
14. <a id="source-14"></a> [VM system disk size](../digests/vm-disk-size.md).
15. <a id="source-15"></a> [Data disk size and offline grow](../digests/data-disk-grow.md).

16. <a id="source-16"></a> [Build identity and host homes](../digests/installation.md).

17. <a id="source-17"></a> [Lima storage and space names](../digests/lima-storage.md).
