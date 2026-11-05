# Confinement and runtime composition

[FACT] Agent workloads run through OpenShell inside the selected space VM. The outer
container has elevated setup privileges; its `nerdctl exec` path bypasses the
inner policy. Package maintenance deliberately uses that path. Agent execution
and long-lived auxiliary workloads must preserve the sandbox boundary.
[1](#source-1)

[FACT] The native OpenShell 0.1.6 integration refuses final images with
an inherited `ENTRYPOINT`; rebuild the base without it or move setup into a
feature/`RUN` step. It also removes private secret-provided proxy environment
settings, even for unconfined boxes. These accepted limits are documented
together; on the integration tree new ordinary boxes in a native space select
0.1.6, while the legacy pins stay until the old path is retired. [15](#source-15)

[FACT] Policy comes from explicit positive capabilities plus requirements of
effective features. `unconfined_network` is a separate process-mode setting that
removes inner network/proxy confinement. UID/GID alignment makes host workdirs
writable without implementing user-namespace remapping. [1](#source-1)

[FACT] Host GIDs may already exist in the image. The entrypoint retains the
named `sandbox` group and permits it to share the numeric host GID with the
existing group, except GID zero, which is rejected before identity changes.
It does not change workdir ownership. [1](#source-1)

[FACT] PTY access is opt-in through `/dev/pts` and `/dev/tty`; writable symlink
paths such as `/dev/ptmx` are rejected by policy composition. Preserve policy
goldens and the runtime feature smoke when changing grants. [1](#source-1)

[FACT] `box show` reports Landlock from the VM kernel's ABI query in the
controller plus the retained OpenShell log. `available` names the kernel ABI and
does not claim per-process enforcement; only demonstrated kernel gaps or
reported sandbox failures print `DEGRADED`. Prove enforcement with a confined
allow/deny file test. [13](#source-13)

[FACT] Feature-composed images combine the base, installation recipe and space
owner's UID/GID. Available sandbox identities are prepared before feature
installation; npm ownership is set in the install layer to avoid duplicating
packages. Matching containers
skip home remapping at startup; images for different identities remain distinct.
Cached composed images are reused; rebuild refreshes feature layers separately
from base refresh. Apt index/deb caches stay outside image layers and refreshes
still validate signed metadata before installation. Agent packages track
`latest`; infrastructure dependencies have deliberate pins. npm and base apt downloads use disposable BuildKit caches
outside the image, with metadata revalidation. Managed rebuilds retain downloads;
custom bases/raw Dockerfile features keep full cache invalidation.
Runtime npm maintenance uses the explicit
sandbox-owned global prefix and maintenance cache. [1](#source-1)

[FACT] The optional `npm` development feature uses the base's Node.js/npm/npx,
sets user-writable global installs and a separate development cache, and grants
scoped public-registry access. Custom bases must supply those runtime tools.
It neither replaces the base's Node version nor opens arbitrary package download
hosts. [5](#source-5)

[FACT] The optional `pip` development feature uses the base's Python 3.13,
verifies venv/pip availability, supplies matching pip commands and writable
user-install/cache directories, and includes the existing PyPI grant. Project
venvs remain explicit; registry-only `pypi` remains separate. [6](#source-6)

[FACT] The optional `go` feature keeps its image's Go (`GOTOOLCHAIN=local` in
Go's own `go.env`, read by every go process) and
grants the module proxy and checksum database to the resolved
`/usr/local/go/bin/go`; policy binaries name real files, never symlinks.
[14](#source-14) [DERIVED] Existing boxes get the grant through reconcile;
`GOTOOLCHAIN=local` is in the composed image and needs a recreate.

[FACT] The Firecracker builder uses the runtime's containerd namespace so
composed builds can resolve locally built and imported base images. [9](#source-9)

[DERIVED] Investigate agent network denial through effective feature/capability
policy, but investigate install/update network behavior through the maintenance
path. They run in different enforcement contexts. [1](#source-1)

[FACT] `yolo.space.operator` explicitly grants broad box authority in the current space:
other existing accounts, granted projects, stronger capabilities and delegation.
A VM controller revalidates the live caller's grant/incarnation. Account, space
and catalog administration remain outside that endpoint. [2](#source-2)

[FACT] A privileged box (`privileged: true`, `--privileged`) is the one
exception to the sandbox, and no capability or feature selects it: the box runs
as a privileged container (full VM devices and module loading) behind yoloctl's
own SSH endpoint, and the sandbox user has sudo. That is space-VM root;
OpenShell policy is recorded, not enforced. Only host-enforced share limits and
read-only drives remain. Only the host starts one; every space operator can use,
restart and recreate it while it runs. It switches mode only through an
explicit `box recreate` and adds no `/dev/kvm`, modules or kernel itself. The
retired `yolo.dev.privileged` capability is reserved and refused, including in
stored declarations; nothing converts it.
leogr reported the nine-stage host gate passing under the capability; the
`--privileged` gate has not been run. [11](#source-11)

[FACT] Its container receives the VM's devices and the bundled kernel builds KVM
in. On a reported AMD host run with nested virtualization, a privileged box
booted, executed in and attached to an inner Firecracker space. Restart-attach
input, inner teardown and Intel hosts remain open.
[11](#source-11) [12](#source-12)

[FACT] Codex includes distro `bubblewrap` in its feature image. Its managed
profile delegates confinement to OpenShell and disables Codex's nested sandbox.
Its OpenAI/ChatGPT grant includes the native Linux Codex executables and detached
daemon across package versions; it retains the existing HTTPS endpoint scope.
[1](#source-1)

[FACT] Gemini uses image-owned settings to disable its self-updater and default
to usage statistics off and shell execution without new PTYs. Its required
network capability names individual API/OAuth endpoints; interactive shell PTYs
remain opt-in.
[3](#source-3)

[FACT] Antigravity (`agy`) uses verified native releases, executable-scoped
egress and OpenShell confinement with its nested sandbox disabled. It adds no
PTY grant. Existing account settings survive launcher initialization. [4](#source-4)

[FACT] Pi uses Node-scoped grants for Anthropic, OpenAI/ChatGPT, Gemini API and
OpenRouter. Managed launches suppress automatic downloads and telemetry, while
explicit model requests remain enabled. Its image includes search tools and
adds no PTY grant. [7](#source-7)

[FACT] OpenCode uses an npm-installed native executable with provider-specific
HTTPS grants and npm plugin installation enabled, including scoped packages.
Registry access is restricted to that executable; custom registries need explicit
grants. It includes ripgrep and no extra PTY grant. Managed launch
disables self-updates, model-catalog fetching, sharing and automatic LSP downloads. [8](#source-8)

[FACT] OCI Dev Container Features install pinned tooling with typed options and
shell/palette completion. They add no runtime grants; incompatible privilege or
lifecycle requirements fail before declaration persistence. [10](#source-10)

[FACT] Opt-in Falco auditing observes the space VM through modern eBPF and
performs no remediation. It is outside box confinement; access is host-only.
Filtered events omit raw argv/environment/buffers, and metadata availability
is not proof that a particular process is confined. Pinned rules report selected
runtime, infrastructure and kernel signals, with explicit noise exceptions;
unknown metadata remains visible. No syscall errno proves which policy denied it.
[16](#source-16)

## Sources

1. <a id="source-1"></a> [Policy, image and maintenance implementation](../digests/policy-images.md).
2. <a id="source-2"></a> [Operator boundary evidence](../digests/operator.md).

3. <a id="source-3"></a> [Gemini adapter](../digests/gemini.md).

4. <a id="source-4"></a> [Antigravity adapter](../digests/agy.md) — native installation, auth and runtime contracts.

5. <a id="source-5"></a> [npm environment](../digests/npm.md) — development configuration, image and egress contracts.

6. <a id="source-6"></a> [pip environment](../digests/pip.md) — Python aliases, venv, user installs and egress contracts.

7. <a id="source-7"></a> [Pi adapter](../digests/pi.md) — install, state, provider and runtime contracts.

8. <a id="source-8"></a> [OpenCode adapter](../digests/opencode.md) — native runtime, account and validation contracts.
9. <a id="source-9"></a> [VM adapter and guest build service](../digests/vm-adapters.md).
10. <a id="source-10"></a> [OCI Features](../digests/oci-features.md).
11. <a id="source-11"></a> [Privileged development boxes](../digests/privileged-boxes.md).
12. <a id="source-12"></a> [VM images](../digests/vm-images.md) — bundled kernel recipe and built-in KVM.
13. <a id="source-13"></a> [Landlock status](../digests/landlock-status.md) — `box show` probe, verdicts and limits.
14. <a id="source-14"></a> [Go environment](../digests/go.md) — resolved binary grant, local toolchain and smoke.
15. <a id="source-15"></a> [OpenShell compatibility](../digests/openshell-compatibility.md) — inherited entrypoints, private proxy environment and pending integration limits.

16. <a id="source-16"></a> [VM-native audit contracts](../digests/audit.md).
