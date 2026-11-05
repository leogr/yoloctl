# OpenShell 0.1.6 image and environment compatibility

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; upstream `ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47` plus `third_party/openshell/patches/`; runtime selection `1d265df` and accompanying changes; mediated signal delivery at `702c6ac7087f9c18a86edb0427fd4349cd82e788` and accompanying changes.

[FACT] On the upgrade integration tree a native space selects the embedded
0.1.6 runtime for every new ordinary (non-privileged) box and for an explicit
`box recreate` of such a box; `restart` keeps a box's recorded runtime. The
ordinary creation path validates an operator grant and, when enabled, creates
the box with a controller-served native registration (see the safety model below).
The active 0.0.72 pins, the legacy Rego embed and the Lima/Firecracker
provisioning of `/opt/openshell` are retired in the same build; the reported
target version becomes v0.1.6. These are constraints of the trial integration,
not a qualified release. [1](#source-1) [2](#source-2) [17](#source-17) [18](#source-18) [19](#source-19)

[FACT] Existing old-runtime containers keep starting through the legacy branch
only while their historical bytes are present: a bounded read-only
compatibility guard holds the two historical binary hashes and refuses a
missing, corrupt or substituted legacy binary before launch or teardown with
"recreate this box to move it to the new OpenShell". Native and privileged
starts skip that guard; privileged creation drops only its unused legacy
interpreter/Rego binds. There is no automatic conversion and no return to the
old runtime. [19](#source-19) [20](#source-20)

[FACT] Safety model of the native path on the integration tree, as decided by
the user: an operator-enabled box gets a native registration published at
creation with its planned incarnation; the controller creates the operator
socket in that registration directory, serves it, rechecks the box's live grant
and incarnation on every request, retires it when the box is removed or the
controller stops, and serves it again after a restart. Native box actions are
authorized by box name, container ID, the yoloctl ownership label and the
per-creation incarnation; no controller-process anchor proof is required.
Cleanup follows the old path: each step's own result decides, anything that
could not be removed is reported by name, and a later box of the same name is
creatable. [22](#source-22)

[FACT] A native box's declared ordinary `env` literals enter the child bootstrap
together with the private account and chat values; a private or chat value
wins over a literal of the same name. No image `ENV` or helper ambient variable
is imported, and private values never appear in inspected ENV, argv or labels.
An existing container takes the change only on explicit recreate. [23](#source-23)

[FACT] Native `box inspect` reports the active policy digest of the qualified
selected generation; when that native read fails the digest is omitted rather
than taken from the stale legacy policy mirror. `--manifest-only` remains a
controller declaration write, not a read-only view. [21](#source-21)

[FACT] The native path requires a final runtime image without an inherited
`ENTRYPOINT`. For example, a base with `ENTRYPOINT ["/usr/local/bin/custom-init"]`
that initializes a directory and then calls `exec "$@"` is unsupported.
Composition inherits the base's image configuration; it does not clear the
entrypoint. The default agentbox base sets no `ENTRYPOINT` or `CMD`.
[3](#source-3) [4](#source-4) [5](#source-5)

[FACT] `PreflightNativeImage` reads the selected final image, rejects a nonempty
entrypoint, and names its configured argv and the remedy: rebuild the base
without `ENTRYPOINT`, or move setup into a feature or Dockerfile `RUN` step.
It neither starts the wrapper nor overrides/clears it. An unavailable or
mismatched inspection also refuses. An empty entrypoint, including the omitted
field emitted by nerdctl, is accepted only with an available configuration.
[3](#source-3) [6](#source-6) [7](#source-7) [14](#source-14)

[FACT] The controller consumes prepared images by bare OCI target digest.
Launch inspection verifies that target through the returned `RepoDigests`,
alongside the same object's distinct configuration digest and native Linux
architecture. It does not compare the target with the configuration `Id` or
substitute a mutable tag. Named tag inspection retains the existing strict GC
matcher; GC behavior is unchanged. [6](#source-6) [15](#source-15) [16](#source-16)

[FACT] The private creation factory calls this read-only preflight before
chat/observation setup, artifact installation, reservation, policy/socket setup
or container creation.
Replacement callers must also call it before session teardown; that public
lifecycle wiring remains pending. After create, the container's identity
(name, space/box/control-root labels, network mode) and a non-privileged report
are still required; the exact launch/bind comparison is a named warning, not a
refusal (old-path parity). A failed create is a plain error, a failed cleanup
after it a named warning; only a lost reply is uncertain: a cancel or timeout
error, or any failure while the call's own context is cancelled (a killed
engine process returns its exit status, not a context error). Starting an existing box's fresh generation follows the same rule:
a failed engine Start is a plain error and a failed cleanup a warning, while a
lost Start reply or a lost activation result stays uncertain. A box made by an earlier build keeps its installed, hash-pinned helper: its
snapshot and next generation give one notice instead of refusing on helper
source identity (`checkExistingBoxHelper`); new creates and helper installs
still require helper bytes matching this binary's sources. Independent helper/PID1/executable/native-record/namespace
authorization remains. Image metadata alone grants no activation authority.
[2](#source-2) [3](#source-3) [8](#source-8) [9](#source-9)

[FACT] OpenShell 0.1.6's child hardening filter denied workload calls that
v0.0.72 allowed, so GNU make (posix_spawn with `POSIX_SPAWN_RESETIDS`), `nice`,
`ionice` and `killpg` failed with EPERM in every box. yoloctl patch 3
(`0003-v0072-syscall-parity.patch`) restores them as in v0.0.72: `capset`,
`setuid`, `setgid`, `setreuid`, `setregid`, `setresuid`, `setresgid`,
`setfsuid`, `setfsgid`, `setgroups`, `setpriority`, `ioprio_set`,
`prlimit64`/`sched_set*` on any process except the sandbox, and
`kill`/`rt_sigqueueinfo` with PID zero or a negative PID (process groups,
`killpg`) except the sandbox's own group. Those sandbox-targeted denies stay:
the sandbox startup probe requires them. Patch 4 (`0004-v0072-process-group-signals.patch`)
restores process-group `kill` in the signal broker (`mediate_process_signal`):
PID zero (the caller's group) or `-PGID` is delivered unless it is the
sandbox's own group; broadcast `kill(-1)` and queued group signals stay
denied, as upstream decided. The workload has no capabilities
and runs with no_new_privs, so the kernel still refuses any real identity or
capability gain; every deny v0.0.72 also had is kept. Releases may carry these
optional third and fourth patches; two- and three-patch releases stay valid
(`qualifiedPatchStack`).
[27](#source-27)

[FACT] Inside a box, a `kill` or `rt_sigqueueinfo` aimed at a positive PID is
seccomp-notified to the sandbox broker, which checks that the notification is
still live (`validate_id`), delivers the signal itself through
`pidfd_send_signal` on a retained pidfd and only then answers the
notification; patch 4's group path (`mediate_group_signal`: `kill(-group)`,
then the answer) keeps that order. Thread-directed signals (`tkill`,
`tgkill`, `rt_tgsigqueueinfo`) are not delivered by the broker: after its
checks it continues them and the kernel performs the call, and the last two
are notified only for `SIGCONT`.
The workload listener is installed with `SECCOMP_FILTER_FLAG_NEW_LISTENER`
alone, without `WAIT_KILLABLE_RECV`, the kernel flag that makes a notifying
process ignore non-fatal signals once the supervisor has received the
notification. Upstream states the consequence: a signal can interrupt a
notified syscall, which the kernel restarts or fails with `EINTR`, and
handlers answer a repeated operation as the kernel would. There is no repeat
detection, so a `kill` interrupted after delivery and restarted arrives as a
new notification and is delivered again. Go workloads take this path:
`pidfd_open` returns `ENOSYS` in a box, so `os.Process.Signal` falls back to
plain `kill`, and Go installs every signal handler with `SA_RESTART`. The
attach client forwards its first signal and cancels its context on a second,
so a duplicate can race the attach's detach reply: the exit status then
depends on whether the final reply arrived before the cancellation.
`commandSignalContext` sends no signal itself, and no yoloctl patch changes
the listener flags or adds repeat detection: the limit is documented, not
fixed. [28](#source-28) [29](#source-29) [30](#source-30) [31](#source-31)
[32](#source-32) [33](#source-33)

[FACT] OpenShell 0.1.6 removes secret-provided `HTTP_PROXY`, `HTTPS_PROXY`,
`ALL_PROXY` and `NO_PROXY` settings, including their lowercase forms, from the
private workload environment even in unconfined boxes. Custom-profile secret
injection does not preserve these variables. An application relying on them
may fail or fall back to other configuration; removing them does not establish
a direct connection. Commands/tools can still configure their own proxy.
Upstream 0.1.6 strips these entries after private environment application,
filters delegated environment, and strips again before explicit exec settings.
[10](#source-10) [11](#source-11) [12](#source-12) [13](#source-13)

[FACT] Project regressions cover default/no-entrypoint images, exec/shell
wrappers, safe error display, unavailable/mismatched metadata and one read-only
inspection with no transaction effects on refusal. These tests do not establish
live custom-image startup or complete runtime parity. [7](#source-7)

[FACT] `make build` obtains the OpenShell sandbox and supervisor by building
them on the host with cargo-zigbuild for both musl targets from the pinned
upstream commit plus the two checked-in patches; the script verifies every
resulting Git tree and the stamped Cargo files against `pins.json`, pins the
Rust toolchain through rustup, records zig/cargo-zigbuild versions, host and
output hashes in the cache receipt, and caches the result by input hash under
`bin/openshell/`. Output bytes are not required to equal the reviewed release
archives; `native-qualify` then embeds the built archives and writes the
qualification selection (manifest, its SHA-256, Rego) to the git-ignored
`internal/nativeartifacts/embedded/selection/`, which the loader prefers over
the committed empty default, so a plain `go build` refuses native boxes with
"run make build". `make build` re-runs that qualification only when the embed
key changes: the OpenShell cache key joined with a hash of the helper's real Go
source closure for linux amd64 and arm64 (`go list -deps`), `go.mod`/`go.sum`,
the Go version, the helper recipe and the qualify scripts. There is no
fallback archive set: without a cached build, missing rustup, zig or
cargo-zigbuild stops `native-embed` with the install hint.
[24](#source-24) [25](#source-25)

[FACT] Withdrawing a box's port forwards (destroy, and the stop/recreate path
through `port.UnforwardAgentbox`) follows the lost-reply rule: a failed stop
reply or a failed post-stop inventory is a plain error that ends the destroy
before container removal; only a stop whose own context ended stays uncertain.
Each acknowledged stop is followed by a best-effort `systemctl reset-failed` of
the transient unit, which `vmservice.Start` runs without `--collect` (microVM
unit manager excepted), and units still listed when
the 5 s post-stop bound ends are reported by name as a warning while the
destroy continues. The cause (a forwarder exiting non-zero on SIGTERM and staying
listed as failed) was reproduced on Linux Lima before the fix, and the fix
confirmed there with a host-side `box destroy --force`; the original macOS
operator-box case was not rerun separately. [26](#source-26)

## Sources

1. <a id="source-1"></a> [assets/embed.go](../../assets/embed.go#L1) — embedded assets; the `SandboxVersion`/`SandboxSHA*` pins and the legacy Rego embed are removed by the retirement delta (tree `3cc5e841`).
2. <a id="source-2"></a> [internal/agentbox/openshell_create_linux.go](../../internal/agentbox/openshell_create_linux.go#L25-L180) — private factory and preflight ordering.
3. <a id="source-3"></a> [internal/agentbox/openshell_image.go](../../internal/agentbox/openshell_image.go#L1-L27) — `PreflightNativeImage`, caller contract and refusal wording.
4. <a id="source-4"></a> [internal/agentboximage/composed.go](../../internal/agentboximage/composed.go#L48-L73) — generated final image starts from the base.
5. <a id="source-5"></a> [assets/containers/agentbox/Dockerfile](../../assets/containers/agentbox/Dockerfile#L106-L113) — default base startup configuration.
6. <a id="source-6"></a> [internal/container/image_launch.go](../../internal/container/image_launch.go#L1-L70) — target/configuration/platform binding and named inspection.
7. <a id="source-7"></a> [internal/agentbox/openshell_image_test.go](../../internal/agentbox/openshell_image_test.go#L1-L227) — real prepared-digest input, refusal, transaction ordering and retained strict evidence regressions.
8. <a id="source-8"></a> [internal/agentbox/openshell_container_plan_linux.go](../../internal/agentbox/openshell_container_plan_linux.go#L125-L189) — post-create identity refusal and launch/bind warning (plain create errors: `openshell_create_transaction.go`).
9. <a id="source-9"></a> [internal/openshell/activation_peer_linux.go](../../internal/openshell/activation_peer_linux.go#L60-L151) — independent actual-process authorization.
10. <a id="source-10"></a> [Upstream process.rs](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/process.rs#L125-L167) — proxy environment names and removal.
11. <a id="source-11"></a> [Upstream delegated.rs](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/delegated.rs#L73-L74) — private delegated environment filtering.
12. <a id="source-12"></a> [Upstream boundary_exec.rs](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/boundary_exec.rs#L169-L189) — private environment, proxy removal and explicit exec settings.
13. <a id="source-13"></a> [Upstream process.rs](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/process.rs#L510-L535) — canonical process environment followed by proxy removal.
14. <a id="source-14"></a> [nerdctl 2.3.5 dockercompat.Config](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/inspecttypes/dockercompat/dockercompat.go#L193-L216) — image configuration and omitted empty entrypoint field.
15. <a id="source-15"></a> [cmd/yoloctl/controller_images.go](../../cmd/yoloctl/controller_images.go#L272-L291) — `imageWarmState.consume` returns the retained OCI target digest.
16. <a id="source-16"></a> [nerdctl 2.3.5 ImageFromNative](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/inspecttypes/dockercompat/dockercompat.go#L650-L692) — target aliases, distinct platform configuration ID and bound image configuration.
17. <a id="source-17"></a> [cmd/yoloctl/openshell_default.go](../../cmd/yoloctl/openshell_default.go#L13-L48) — `defaultCreationContext`, `qualifyDefaultNativeCreation`, `checkDefaultNativeCreation` (native space, not privileged; the operator grant is validated, no longer refused).
18. <a id="source-18"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L378-L400) — `LiveApplier.Recreate` ends in `Create`, which takes the native selection when bound; `Restart` keeps the recorded runtime.
19. <a id="source-19"></a> [internal/container](../../internal/container/container.go#L1) — historical compatibility guard in `legacy_openshell.go` (added by the retirement delta): script L16, the two historical hashes L20–L21, bounded VM read L30; `cmd/yoloctl/version.go` reports v0.1.6 (retirement tree `3cc5e841`).
20. <a id="source-20"></a> [internal/agentbox](../../internal/agentbox/live.go#L1) — `ErrLegacyOpenShellUnavailable` in `openshell_legacy.go` (added by the retirement delta) ("recreate this box to move it to the new OpenShell"), legacy launch/teardown preflight; privileged bind trimming in live.go.
21. <a id="source-21"></a> [internal/agentbox/openshell_policy_read.go](../../internal/agentbox/openshell_policy_read.go#L1) — `LiveInspector.ReadRuntimePolicy`; `buildInspectRuntime` in cmd/yoloctl/agentbox_inspect.go (policy-view tree `ad168599`); `TestRuntimePolicyViewNeverHashesLegacyMirrorForNativeFailure`.
22. <a id="source-22"></a> [internal/controller](../../internal/controller/server.go#L1) — `ServeNativeOperator`, `CloseNativeOperators` in `operator_native_serve.go`, `retireOperatorListener` in `operator_retire.go`; `operator.ProvisionNative` in internal/operator/native_publisher_linux.go; warning-only operator cleanup steps in internal/agentbox/openshell_remove_linux.go.
23. <a id="source-23"></a> [internal/agentbox/openshell_container_plan_linux.go](../../internal/agentbox/openshell_container_plan_linux.go#L1) — literal env snapshot merged under private/chat envelope values; `openshell_literal_environment_linux_test.go` (sol native-literal-env tree `e17b88f6`).
24. <a id="source-24"></a> [scripts](../../scripts/guest-hashes.go#L1) — `scripts/openshell-build.py` (`toolchain`, `prepare_source`, `build`, `write_qualify_inputs`) and `third_party/openshell/pins.json`; Makefile `native-embed`.
25. <a id="source-25"></a> [scripts](../../scripts/guest-hashes.go#L1) — `scripts/openshell-embed-key.py` (`closure_files`, `helper_hash`) and the Makefile `native-embed` rule.
26. <a id="source-26"></a> [internal/agentbox/openshell_withdraw.go](../../internal/agentbox/openshell_withdraw.go#L1) — `WithdrawNativeForwards` lost-reply rule, leftover warning, `vmservice.Collect`; `port.UnforwardAgentbox` in internal/port/port.go.
27. <a id="source-27"></a> [third_party/openshell/patches/0003-v0072-syscall-parity.patch](../../third_party/openshell/patches/0003-v0072-syscall-parity.patch#L1) — restored calls (header) against upstream `crates/openshell-sandbox/src/linux/child_seccomp.rs` `prepare` and v0.0.72 `crates/openshell-supervisor-process/src/sandbox/linux/seccomp.rs`; `qualifiedPatchStack` in `internal/openshell/release.go`.
28. <a id="source-28"></a> [Upstream seccomp_notify.rs](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/linux/seccomp_notify.rs#L170-L176) — `NotificationListener`: installed without `WAIT_KILLABLE_RECV`, interrupted syscalls restart; `install_workload_listener` notified syscalls L333-L356; `install_notification_filter` flags `NEW_LISTENER` only, L636-L650; `build_filter` notifies `tgkill`/`rt_tgsigqueueinfo` only for `SIGCONT`, L665-L745.
29. <a id="source-29"></a> [Upstream process_signal.rs](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/linux/process_signal.rs#L20-L85) — `mediate_process_signal`: `validate_id`, `pidfd_send_signal`, then `respond_value`; `mediate_thread_signal` L88-L129 ends in `respond_continue`; the group path `mediate_group_signal` (`kill(-group)`, then `respond_value`) is added by `third_party/openshell/patches/0004-v0072-process-group-signals.patch`.
30. <a id="source-30"></a> [Upstream child_seccomp.rs](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/linux/child_seccomp.rs#L150-L201) — `pidfd_send_signal` denied to the workload, `pidfd_open` answered with `ENOSYS`.
31. <a id="source-31"></a> [Go 1.27.3 os/exec_unix.go](https://github.com/golang/go/blob/go1.27.3/src/os/exec_unix.go#L77-L91) — `(*Process).signal`: pidfd when `checkPidfd` passes (`os/pidfd_linux.go` L154-L197), otherwise `kill`; `runtime/os_linux.go` L480 sets `SA_RESTART` on every handler.
32. <a id="source-32"></a> [Linux v6.12 seccomp_filter.rst](https://github.com/torvalds/linux/blob/v6.12/Documentation/userspace-api/seccomp_filter.rst#L276-L282) — `SECCOMP_FILTER_FLAG_WAIT_KILLABLE_RECV` semantics.
33. <a id="source-33"></a> [cmd/yoloctl/signals.go](../../cmd/yoloctl/signals.go#L17-L50) — `commandSignalContext`: first signal forwarded, a second cancels; `TestAttachClientSignalDuringOwnedSetupDetaches` in `attach_signal_test.go` sends one `Process.Signal` and expects status 75.
