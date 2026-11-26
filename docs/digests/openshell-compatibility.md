# OpenShell image and environment compatibility

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; v0.1.10 runtime and recorded releases at `f357061bf0f425f6d0a007d6fb31f575f038c827` and accompanying changes, upstream v0.1.10 `152c59d3178039e3fbb4a5595f9fff7216a6be92` plus `third_party/openshell/patches/`; runtime selection `1d265df` and accompanying changes; mediated signal delivery at `702c6ac7087f9c18a86edb0427fd4349cd82e788` and accompanying changes; its measurement and `signalOnce` at `658ead276` and accompanying changes; main-output backpressure at `fa7a6fe46880fd7232f8ed7eda0e81ccc934d389` and accompanying changes. nerdctl inspect sources re-read at `fcd4a112` and accompanying changes.

[FACT] A native space selects the embedded OpenShell v0.1.10 runtime
(`github.com/ai-openshell/openshell`, with eight yoloctl patches) for every new
ordinary (non-privileged) box and for an explicit `box recreate` of such a box; `restart` keeps a native box's runtime and
refuses a pre-native one. The
ordinary creation path validates an operator grant and, when enabled, creates
the box with a controller-served native registration (see the safety model below).
The active 0.0.72 pins, the legacy Rego embed and the Lima/Firecracker
provisioning of `/opt/openshell` were retired with the move to 0.1.6.
[1](#source-1) [2](#source-2) [17](#source-17) [18](#source-18) [19](#source-19)

[FACT] A box keeps the runtime it recorded until it is recreated. New creates
and helper installs accept only the current v0.1.10 source; reads of an
existing box (runtime inspection, `space upgrade`'s helper check, historical
origins) also accept the recorded OpenShell 0.1.6 source under its own rules:
two to nine patches (v0.1.0–v0.3.0 recorded four, v0.4.x nine), bootstrap
schema 2 and protocol fixture `workspace-read-only-v1`. Such a box is stopped,
started and restarted with its own helper, runtime and rules file, and
`space upgrade` offers its recreate. [37](#source-37)

[FACT] Existing old-runtime containers are never started or restarted again:
recreation is their only route to a new run, with no automatic conversion and
no return to the old runtime ([pre-native boxes](pre-native-boxes.md)). The
historical-binary compatibility guard is removed; privileged creation drops
only its unused legacy interpreter/Rego binds. Such a box warns on every
one-box command (see [stop-box](stop-box.md)). [19](#source-19) [20](#source-20)

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

[FACT] OpenShell's child hardening filter (0.1.6 and v0.1.10) denies workload calls that
v0.0.72 allowed, so GNU make (posix_spawn with `POSIX_SPAWN_RESETIDS`), `nice`,
`ionice` and `killpg` failed with EPERM in every box. yoloctl patch 3
(`0003-v0072-syscall-parity.patch`) restores them as in v0.0.72: `capset`,
`setuid`, `setgid`, `setreuid`, `setregid`, `setresuid`, `setresgid`,
`setfsuid`, `setfsgid`, `setgroups`, `ioprio_set`; `prlimit64`,
`sched_set*` and `setpriority` on any named process except the sandbox; and
`kill`/`rt_sigqueueinfo` with PID zero or a negative PID (process groups,
`killpg`) except the sandbox's own group. Those sandbox-targeted denies stay:
the sandbox startup probe requires them. Patch 4 (`0004-v0072-process-group-signals.patch`)
restores process-group `kill` in the signal broker (`mediate_process_signal`):
PID zero (the caller's group) or `-PGID` is delivered unless it is the
sandbox's own group; broadcast `kill(-1)` and queued group signals stay
denied, as upstream decided. Since v0.1.10 (#5212) upstream sends a
scheduling, priority or limit call that names a target to the broker
(`mediate_process_sched`), which allows only the caller's own thread group;
patch 3 widens that rule for a named process target to any target outside
the trusted sandbox's thread group, so `renice` and `ionice -p` act on other
workload processes as before, and the broker's membership check protects
sandbox worker threads, not only the leader, for named targets. `setpriority`
with `PRIO_PGRP` or `PRIO_USER` is continued to the kernel without that
membership check, as the 0.1.6 filter allowed it. The sandbox runs as the
workload's user, so a group or user selection that includes it (`renice -g`,
`renice -u`) reaches it too, as on 0.1.6 and v0.0.72. The workload has no capabilities
and runs with no_new_privs, so the kernel still refuses any real identity or
capability gain; every deny v0.0.72 also had is kept. Releases may carry these
optional third and fourth patches; two- and three-patch releases stay valid
(`qualifiedPatchStack`).
[27](#source-27) [38](#source-38)

[FACT] Patch 5 (`0005-main-raw-input.patch`) adds a per-attachment switch to the
`openshell-main` subsystem: the SSH env request `OPENSHELL_MAIN_RAW_INPUT=1`
forwards every input byte to the canonical main process, bypassing upstream's
Ctrl-D and Ctrl-P Ctrl-Q detach filter. The subsystem request succeeds only
once the attachment holds the stdin lease; when another attachment owns stdin,
the main process is unavailable, or `OPENSHELL_MAIN_READ_ONLY=1` is also set, the
request itself fails before the diagnostic and status 1, never attaching
read-only. The lease is recorded on the channel before any await, so a dropped
handler releases it. Without the switch the behaviour is upstream's. Releases may carry it
as an optional fifth patch. [34](#source-34)

[FACT] Patch 6 (`0006-main-attachments-ended.patch`) leaves the main exit
marker where it is and, once upstream's terminal-delivery wait completes, writes
`main-attachments-ended` (owner-only, atomic, content `ended` and a newline) in
the marker's directory. That wait ends when every `openshell-main` attachment
present at the exit has ended, which in russh 0.64 and 0.65 is its SSH connection ending:
the server closes the channel after the exit status, so a client's channel
close alone does not end the attachment. With no attachment the file follows at
once; a shutdown during the wait writes nothing. The file does not tell
completion from abandonment and carries no timer. Releases may carry it as an
optional sixth patch after the fifth. [35](#source-35)

[FACT] Patch 7 (`0007-main-output-backpressure.patch`) paces canonical-main
output by the supervisor's input-owner cursor; viewers retain bounded replay/lag.
The sandbox waits while its stream subscriber exists, preserving upstream's
single consumption position. Release/drop and teardown unblock publication;
exec's 30-second failure rule and both 1 MiB rings are unchanged.

[FACT] At main exit the sandbox continues to EIO/EOF, or after 2 seconds captures
one readable-byte count per descriptor under its reader lock (at most 1 MiB,
plus its held chunk). This fixed cutoff runs even while publication is blocked;
the captured tail stays paced. Count/read errors are logged and end capture,
preserving bytes already read. Raw process status becomes available before the
owned tail reaper finishes. The supervisor waits for that tail, restarting its
existing 2-second deadline on input-owner cursor progress; without an owner the
original total wait applies. Progress is SSH-layer acceptance, not holder receipt.
The lifecycle shutdown also cancels this extended wait before publishing a
completion marker. On idle expiry old output may be skipped: the owner gets a diagnostic and the
retained Exit, while other lagging viewers still close. Output cannot follow or
evict Exit. PID 1's separate 4-second post-marker bound and patch 6 are unchanged;
slow readers can still lose output. This is source/unit evidence, requiring final
holder/VM qualification. When a stop wins, the main-exit report is skipped
and the tail wait is cancelled, as before; v0.1.10's own exit report on stop
is not taken, while its relay flush, bounded by the 8-second shutdown
deadline, is kept (the relay is inactive in yoloctl). [36](#source-36)

[FACT] Inside a box, a `kill` or `rt_sigqueueinfo` aimed at a positive PID is
seccomp-notified to the sandbox broker, which checks that the notification is
still live (`validate_id`), delivers the signal to another process itself through
`pidfd_send_signal` on a retained pidfd and only then answers the
notification; since v0.1.10 (#5212) a signal to the caller's own thread group
is instead continued, so the kernel performs it with the workload's own
`si_pid`; patch 4's group path (`mediate_group_signal`: `kill(-group)`,
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
so a duplicate can race the attach's detach reply. Measured in a box: a
single `kill` reached a Go or Python child twice (occasionally three times) in
about 1% of trials, and `sigtimedwait` reported every copy with the broker
(`openshell-sandbox`) as `si_pid`; a thread-directed `tgkill` to the child's
main thread arrived once in 8,000 trials (measured on 0.1.6; the cross-process
path is unchanged in v0.1.10). `commandSignalContext` sends no
signal itself, and no yoloctl patch changes the listener flags or adds repeat
detection: the limit is documented, not fixed. The signal regressions send
their one test signal with `signalOnce` (`tgkill` on Linux). [28](#source-28) [29](#source-29) [30](#source-30) [31](#source-31)
[32](#source-32) [33](#source-33)

[FACT] OpenShell (0.1.6 and v0.1.10) removes secret-provided `HTTP_PROXY`, `HTTPS_PROXY`,
`ALL_PROXY` and `NO_PROXY` settings, including their lowercase forms, from the
private workload environment even in unconfined boxes. Custom-profile secret
injection does not preserve these variables. An application relying on them
may fail or fall back to other configuration; removing them does not establish
a direct connection. Commands/tools can still configure their own proxy.
Upstream strips these entries after private environment application,
filters delegated environment, and strips again before explicit exec settings.
[10](#source-10) [11](#source-11) [12](#source-12) [13](#source-13)

[FACT] Project regressions cover default/no-entrypoint images, exec/shell
wrappers, safe error display, unavailable/mismatched metadata and one read-only
inspection with no transaction effects on refusal. These tests do not establish
live custom-image startup or complete runtime parity. [7](#source-7)

[FACT] `make build` obtains the OpenShell sandbox and supervisor by building
them on the host with cargo-zigbuild for both musl targets from the pinned
upstream commit plus the ordered checked-in patches; the script verifies every
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
5. <a id="source-5"></a> [assets/containers/agentbox/Dockerfile](../../assets/containers/agentbox/Dockerfile#L103-L110) — default base startup configuration.
6. <a id="source-6"></a> [internal/container/image_launch.go](../../internal/container/image_launch.go#L1-L69) — target/configuration/platform binding and named inspection.
7. <a id="source-7"></a> [internal/agentbox/openshell_image_test.go](../../internal/agentbox/openshell_image_test.go#L1-L227) — real prepared-digest input, refusal, transaction ordering and retained strict evidence regressions.
8. <a id="source-8"></a> [internal/agentbox/openshell_container_plan_linux.go](../../internal/agentbox/openshell_container_plan_linux.go#L139-L203) — post-create identity refusal and launch/bind warning (plain create errors: `openshell_create_transaction.go`).
9. <a id="source-9"></a> [internal/openshell/activation_peer_linux.go](../../internal/openshell/activation_peer_linux.go#L60-L151) — independent actual-process authorization.
10. <a id="source-10"></a> [Upstream process.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-sandbox/src/process.rs#L133-L175) — proxy environment names and removal.
11. <a id="source-11"></a> [Upstream delegated.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-sandbox/src/delegated.rs#L74-L75) — private delegated environment filtering.
12. <a id="source-12"></a> [Upstream boundary_exec.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-sandbox/src/boundary_exec.rs#L176-L206) — private environment, proxy removal and explicit exec settings.
13. <a id="source-13"></a> [Upstream process.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-sandbox/src/process.rs#L289-L334) — canonical process environment followed by proxy removal (`compose_canonical_process_environment`).
14. <a id="source-14"></a> [nerdctl 2.4.2 dockercompat.Config](https://github.com/containerd/nerdctl/blob/v2.4.2/pkg/inspecttypes/dockercompat/dockercompat.go#L209-L229) — image configuration and omitted empty entrypoint field.
15. <a id="source-15"></a> [cmd/yoloctl/controller_images.go](../../cmd/yoloctl/controller_images.go#L272-L291) — `imageWarmState.consume` returns the retained OCI target digest.
16. <a id="source-16"></a> [nerdctl 2.4.2 ImageFromNative](https://github.com/containerd/nerdctl/blob/v2.4.2/pkg/inspecttypes/dockercompat/dockercompat.go#L717-L760) — target aliases, distinct platform configuration ID and bound image configuration.
17. <a id="source-17"></a> [cmd/yoloctl/openshell_default.go](../../cmd/yoloctl/openshell_default.go#L13-L48) — `defaultCreationContext`, `qualifyDefaultNativeCreation`, `checkDefaultNativeCreation` (native space, not privileged; the operator grant is validated, no longer refused).
18. <a id="source-18"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L380-L402) — `LiveApplier.Recreate` ends in `Create`, which takes the native selection when bound; `Restart` is native-only.
19. <a id="source-19"></a> [cmd/yoloctl/version.go](../../cmd/yoloctl/version.go#L1) — reports the runtime version, v0.1.10 (`assets/embed.go` `SandboxVersion`).
20. <a id="source-20"></a> [internal/agentbox/openshell_legacy.go](../../internal/agentbox/openshell_legacy.go#L1) — `LegacyRuntimeError`, `LegacyRuntime`, `withoutLegacyOpenShellBinds` (privileged bind trimming).
21. <a id="source-21"></a> [internal/agentbox/openshell_policy_read.go](../../internal/agentbox/openshell_policy_read.go#L1) — `LiveInspector.ReadRuntimePolicy`; `buildInspectRuntime` in cmd/yoloctl/agentbox_inspect.go (policy-view tree `ad168599`); `TestRuntimePolicyViewNeverHashesLegacyMirrorForNativeFailure`.
22. <a id="source-22"></a> [internal/controller](../../internal/controller/server.go#L1) — `ServeNativeOperator`, `CloseNativeOperators` in `operator_native_serve.go`, `retireOperatorListener` in `operator_retire.go`; `operator.ProvisionNative` in internal/operator/native_publisher_linux.go; warning-only operator cleanup steps in internal/agentbox/openshell_remove_linux.go.
23. <a id="source-23"></a> [internal/agentbox/openshell_container_plan_linux.go](../../internal/agentbox/openshell_container_plan_linux.go#L1) — literal env snapshot merged under private/chat envelope values; `openshell_literal_environment_linux_test.go` (sol native-literal-env tree `e17b88f6`).
24. <a id="source-24"></a> [scripts](../../scripts/guest-hashes.go#L1) — `scripts/openshell-build.py` (`toolchain`, `prepare_source`, `build`, `write_qualify_inputs`) and `third_party/openshell/pins.json`; Makefile `native-embed`.
25. <a id="source-25"></a> [scripts](../../scripts/guest-hashes.go#L1) — `scripts/openshell-embed-key.py` (`closure_files`, `helper_hash`) and the Makefile `native-embed` rule.
26. <a id="source-26"></a> [internal/agentbox/openshell_withdraw.go](../../internal/agentbox/openshell_withdraw.go#L1) — `WithdrawNativeForwards` lost-reply rule, leftover warning, `vmservice.Collect`; `port.UnforwardAgentbox` in internal/port/port.go.
27. <a id="source-27"></a> [third_party/openshell/patches/0003-v0072-syscall-parity.patch](../../third_party/openshell/patches/0003-v0072-syscall-parity.patch#L1) — restored calls (header) against upstream `crates/openshell-sandbox/src/linux/child_seccomp.rs` `prepare` and v0.0.72 `crates/openshell-supervisor-process/src/sandbox/linux/seccomp.rs`; `qualifiedPatchStack` in `internal/openshell/release.go`.
28. <a id="source-28"></a> [Upstream seccomp_notify.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-sandbox/src/linux/seccomp_notify.rs#L186-L192) — `NotificationListener`: installed without `WAIT_KILLABLE_RECV`, interrupted syscalls restart; `install_workload_listener` notified syscalls L357-L384; `install_notification_filter` flags `NEW_LISTENER` only, L665-L692; `build_filter` notifies `tgkill`/`rt_tgsigqueueinfo` only for `SIGCONT`, L694-L775.
29. <a id="source-29"></a> [Upstream process_signal.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-sandbox/src/linux/process_signal.rs#L39-L111) — `mediate_process_signal`: `validate_id`, own-group `respond_continue` (#5212), otherwise `pidfd_send_signal`, then `respond_value`; `mediate_thread_signal` L123-L154 ends in `respond_continue`; the group path `mediate_group_signal` (`kill(-group)`, then `respond_value`) is added by `third_party/openshell/patches/0004-v0072-process-group-signals.patch`.
30. <a id="source-30"></a> [Upstream child_seccomp.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-sandbox/src/linux/child_seccomp.rs#L186-L237) — `pidfd_send_signal` denied to the workload, `pidfd_open` answered with `ENOSYS`.
31. <a id="source-31"></a> [Go 1.27.3 os/exec_unix.go](https://github.com/golang/go/blob/go1.27.3/src/os/exec_unix.go#L77-L91) — `(*Process).signal`: pidfd when `checkPidfd` passes (`os/pidfd_linux.go` L154-L197), otherwise `kill`; `runtime/os_linux.go` L480 sets `SA_RESTART` on every handler.
32. <a id="source-32"></a> [Linux v6.12 seccomp_filter.rst](https://github.com/torvalds/linux/blob/v6.12/Documentation/userspace-api/seccomp_filter.rst#L276-L282) — `SECCOMP_FILTER_FLAG_WAIT_KILLABLE_RECV` semantics.
33. <a id="source-33"></a> [cmd/yoloctl/signals.go](../../cmd/yoloctl/signals.go#L17-L50) — `commandSignalContext`: first signal forwarded, a second cancels; `signalOnce` in `signal_once_linux_test.go` (`tgkill`), used by `attach_signal_test.go`, `signals_test.go` and `signals_tui_test.go`.
34. <a id="source-34"></a> [Patch 5](../../third_party/openshell/patches/0005-main-raw-input.patch#L1-L480) — `filter_main_detach_sequence` bypass, `main_raw_input`, `refuse_raw_main` and its five tests; `internal/openshell/release.go` `qualifiedPatch5Name`, `qualifiedPatchStack`; release_test.go `TestQualifiedPatchStackAcceptsOptionalFifthPatch`.
35. <a id="source-35"></a> [Patch 6](../../third_party/openshell/patches/0006-main-attachments-ended.patch#L1-L303) — `drain_then_mark_attachments_ended`, `persist_main_attachments_ended` and four tests in `openshell-supervisor/src/lib.rs`; ssh.rs `main_delivery_completes_*` pin the connection-end semantics; `internal/openshell/release.go` `qualifiedPatch6Name`; release_test.go `TestQualifiedPatchStackAcceptsOptionalSixthPatch`.
36. <a id="source-36"></a> [Patch 7](../../third_party/openshell/patches/0007-main-output-backpressure.patch#L1) — `MainTailCapture`, `observe_main_exit`, owned boundary `output_reaper`, `finish_remote_with_timeout`, `publish_main`, `publish_paced`, `acquire_input_for_output`, finite capture/cursor-idle/SSH skip-status controls; `internal/openshell/release.go` `qualifiedPatch7Name`, `qualifiedPatchStack`; release_test.go `TestQualifiedPatchStackAcceptsOptionalSeventhAndEighthPatches`.
37. <a id="source-37"></a> [internal/openshell/release.go](../../internal/openshell/release.go#L1) — `currentRelease`, `recordedReleases` (0.1.6), `LoadRecordedRelease`; callers `validateOpenShellRuntime` (internal/agentbox/openshell_native_linux.go) and `validateNativeHistoricalManifest`; `TestRecordedOpenShell016ManifestsStayReadableForExistingBoxes`.
38. <a id="source-38"></a> [Upstream process_sched.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-sandbox/src/linux/process_sched.rs#L1) — `mediate_process_sched` and `MEDIATED_PROCESS_SCHED_SYSCALLS`; patch 3 adds the sandbox-group rule and `named_target_is_allowed_unless_it_is_the_sandbox`.
