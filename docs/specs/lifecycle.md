# Lifecycle and concurrency

[FACT] Explicit box reconcile is driven by its manifest and inspected runtime. Material
container changes recreate; policy changes generally restart, with workdir
read-only transitions requiring recreation; ports can update live. Agent args
apply at the next session. Account contents do not trigger reconcile. CPU/memory
limits are compared in normalized units, including removal of an explicit cap.
Primary workdir detection uses recorded runtime arguments, keeping additional
project mounts distinct; uncertain workdir metadata refuses reconciliation.
Start/restart preserves existing containers; starting an already-running box is
a no-op. Starting a stopped box or restarting refuses changes requiring
replacement, before teardown where detected. Post-start failures are returned
without replacement; explicit reconcile/recreate applies replacement changes.
Missing containers can still materialize on start/restart.
[1](#source-1) [2](#source-2)

[FACT] Ordinary container start/stop/removal and maintenance/legacy-port execs
use the exact inspected full ID, so a hex-like box name cannot select a peer's
ID prefix. Names remain cleanup/endpoint keys; scoped native-ID ownership and
confined SSH commands retain their existing contracts. [1](#source-1)

[FACT] A box's privileged/confined launch mode is create-only drift classified
from runtime evidence, never a label alone. Only an explicit `box recreate`
switches it; reconcile, set, edit, rebuild and start refuse. Policy-only changes
never restart a proved privileged box because its policy is not enforced. A
privileged endpoint hangs up a disconnected session instead of orphaning it.
[14](#source-14)

[FACT] Runtime teardown includes holder, session artifacts, ports and orphan
network cleanup. A running container does not prove a living agent session.
Graceful stop signals workloads through confined SSH before the runtime's
bounded stop/escalation; it does not depend on PID 1 forwarding SIGTERM.
A sandbox socket inode does not prove a listening SSH endpoint. Readiness
returns a confirmed container exit promptly with its code and a logs hint.
Use the managed applier, shared resolver and actual readiness probe. [1](#source-1)
[3](#source-3) [4](#source-4)

[FACT] Host lifecycle locks and native controller mutation locks are separate.
Native space locks precede entity locks and span apply/update work. Image
preparation runs outside both guards, sharing controller jobs under a VM-sized
cap; apply consumes retained immutable bytes. Cancelled private attempts release
their slot immediately, with best-effort BuildKit cancellation. Npm latest-version
probes run before apply, bound to the current profile/catalog snapshot; native
probes and installation remain in apply.
[18](#source-18) Attach
streaming and editor waits release them; editor saves detect concurrent changes.
Space recreation preserves account disk/declarations and starts a fresh VM,
but boxes require explicit starts. Destruction removes runtime before the disk
and bundle, retaining recovery metadata after intermediate failures. VM
termination proof and disk operations go through the selected adapter. An
uncertain Firecracker launch cannot authorize disk deletion.
[1](#source-1) [6](#source-6)

[FACT] Interrupted commands are never replayed. The controller resolves only
reviewed durable evidence or exact read-only postconditions; unknown outcomes
hold their affected resources and dependencies for host review. Receipt-storage
failures hold all writes until reconciliation; maintenance stays explicit.
[17](#source-17)

[FACT] Plain Firecracker recreation retains its selected VM image. Explicit
`space recreate --update-vm-image` (or `--vm-image DIR`) verifies replacement
media before teardown and updates its controller while retaining native data.
A durable checkpoint supports explicit retry or compatible-image recovery with
`--rollback-vm-image`; ordinary commands cannot bypass pending recovery. Automatic
image cache identity includes recipe inputs, while existing spaces never silently
switch media. Controller-only updates do not replace the kernel/root image.
[8](#source-8)

[DERIVED] A CA trust fix in the VM recipe therefore reaches an existing
Firecracker space only through explicit image replacement, and a Lima space
through `space recreate`. Box images need `image refresh`, then
`box reconcile BOX --rebuild` for each affected box; a plain recreate reuses an
existing composed image. The VM's Go registry clients predate Go's
intermediate-key distrust, so the repaired store protects only their default
verification: custom trust that re-adds a retired root is not covered, and
OpenShell's proxy trust is outside this repair. [8](#source-8) [15](#source-15)

[FACT] AIRC's selected compiled verifier separately rejects compromised emSign
intermediate keys, including under supplied anchors. The source pin affects
feature image builds; existing boxes use it only after explicit rebuild/recreate.
[15](#source-15)

[FACT] Lima container-stack pins reach existing spaces through the explicit
host sequence: update the controller, recreate the space, then start selected
boxes. Controller update alone leaves instance provisioning intact; an old
unpinned instance may follow changing host Lima defaults. See the
[Lima upgrade and preservation boundary](../digests/vm-adapters.md#lima-stack-upgrades).
[6](#source-6)

[FACT] A controller update leaves running sessions on their previous holder,
which the new build cannot attach to. `space start --update-controller` then
lists those boxes from the holders' own answers and restarts them only after an
explicit terminal yes or `--restart-sessions`, through ordinary `box restart`
bound to the listed holder and refused if that session changed. A restarted
managed workload runs again on return: the chat server through one `chat ensure`
that must report the server ready, another `session_command` through the shared
session start; a failure exits nonzero with the recovery command. Scripts only
get the commands, with those recovery steps. A restart ends the agent process;
conversations are resumed explicitly. A listed box that also requires
recreation, which a restart refuses, is left running with its explicit
recreation commands, and an attach refused by an older holder names the remedy
full drift inspection proves (restart, explicit recreation, or undetermined
with `box inspect`). [11](#source-11) [13](#source-13)

[FACT] Export captures declarations, native account files and declared managed
chat data after admission is drained and workloads are stopped. Import restores
into a fresh disk/identity,
can select another VM adapter, and leaves boxes stopped. Host projects, catalogs,
VM runtime and sessions are not archived. Recovery state never serves ordinary
reads. Export omits native sockets and known Codex process artifacts, retains
saved history/settings, and makes internal absolute links portable without
following them. Clean outside absolute symlinks are retained verbatim; their
target contents are not archived. The old schema-2 migration
command is removed. Managed room imports use fresh identities and credentials;
desired enrollment survives, while runtime application remains pending.
Agent conversation records are not archived: an imported space starts fresh
conversation tracking, so the first managed attach assigns a new conversation
and nothing is reported as resumed; imported transcripts stay available for
manual recovery. Same-disk space recreate keeps the records. [7](#source-7)
[10](#source-10) [12](#source-12)

[DERIVED] For a lifecycle failure, separate desired state, runtime availability,
session state and storage identity before retrying. A reachable controller can
show declarations when container runtime is unknown; stopped controllers cannot.
Destructive operations require an authoritative inventory. `box destroy` retains
the declaration and returns a runtime probe error even with `--force`; reusing a
UI fallback for deletion would discard that safety distinction.
[1](#source-1) [3](#source-3)

[FACT] Explicit deployment configures bounded BuildKit cache retention; the
running controller collects unreferenced composed images. Declarations and all existing
containers protect their images; incomplete inventories prevent deletion.
Before a build/pull, low root-disk space triggers cleanup and a measured free-space
check. User data and stopped boxes are retained; cleanup cannot guarantee space
for workloads whose retained data exceeds the VM's capacity. [5](#source-5)

[FACT] Host `space storage` is a lock-free controller read that partitions the
system disk into each container's writable layer, images, BuildKit cache and
the remainder, reporting unmeasured parts instead of folding them into the
remainder. Space view warns before either VM disk runs out. Neither frees
space. [16](#source-16)

[FACT] Firecracker separately measures host capacity for its sparse writable
disks: startup requires 4 GiB available. Its existing helper pauses the VMM below
2 GiB or when measurement fails, and resumes after both backing filesystems have
4 GiB. Paused spaces expose the reason and reject normal lifecycle work until
recovery; they must not be force-killed by graceful-stop fallback. This periodic
guard reserves no capacity and cannot prevent every host-space race. Older
running helpers need a normal VM restart to acquire it. [9](#source-9)

[FACT] Firecracker controller crashes can recover automatically after verified
worker cleanup, with five delayed attempts per failure episode. Uptime of at
least 60 seconds resets the budget; repeated slow crashes can therefore continue.
Clean exit, intentional stop and poweroff suppress restart; stop joins the old
run. Cleanup failure blocks replacement. `active` describes a process, while
backoff/cleanup is `activating`; neither proves readiness. Existing spaces need
`space recreate NAME --update-vm-image`, preserving native data and losing
sessions/ephemeral workdirs. Controller-only updates cannot replace PID 1.
[19](#source-19)

[FACT] Install never runs a space lifecycle command: it prints each space's
commands for the user to run, and `space start`/`recreate` take no installer
consent input. [20](#source-20)

## Sources

1. <a id="source-1"></a> [Reconcile, locking and teardown evidence](../digests/lifecycle.md).
2. <a id="source-2"></a> [Account-content semantics](../digests/auth.md).
3. <a id="source-3"></a> [Resolver failure contracts](../digests/cli-state.md).
4. <a id="source-4"></a> [Holder and attachment evidence](../digests/sessions.md).
5. <a id="source-5"></a> [Storage cleanup and pressure checks](../digests/storage-gc.md).
6. <a id="source-6"></a> [Adapter process and disk proof](../digests/vm-adapters.md).
7. <a id="source-7"></a> [Archive and restore contracts](../digests/space-archives.md).
8. <a id="source-8"></a> [VM image selection and replacement](../digests/vm-images.md).
9. <a id="source-9"></a> [Host storage pressure and recovery](../digests/host-storage.md).
10. <a id="source-10"></a> [Managed chat archive boundary](../digests/managed-chat.md).
11. <a id="source-11"></a> [Sessions after a controller update](../digests/controller-update-sessions.md).
12. <a id="source-12"></a> [Agent conversation identity](../digests/agent-conversations.md).
13. <a id="source-13"></a> [Remedies for sessions of another build](../digests/session-remedies.md).
14. <a id="source-14"></a> [Privileged development boxes](../digests/privileged-boxes.md).
15. <a id="source-15"></a> [CA trust stores](../digests/trust-stores.md).
16. <a id="source-16"></a> [VM disks in space view and `space storage`](../digests/space-view-disks.md).

17. <a id="source-17"></a> [Interrupted-outcome recovery](../digests/controller-recovery.md).

18. <a id="source-18"></a> [Controller image preparation](../digests/image-preparation.md).
19. <a id="source-19"></a> [Controller supervision](../digests/controller-supervision.md) — failure episodes, stop and image rollout.

20. <a id="source-20"></a> [Installation and space advice](../digests/installation.md).
