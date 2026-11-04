# Privileged development boxes

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Only the box's explicit `privileged: true` setting selects a privileged
box: `--privileged` on create, copy and set (`=false` clears it), or an edit.
Capabilities and features never do, and the managed chat server may not set it.
The retired `yolo.dev.privileged` name is reserved: no built-in or custom
capability has it, an explicit or feature-required grant fails with guidance,
and nothing converts it. A stored declaration that still lists it parses as
written and fails validation with or without the setting, so save, reconcile
and rebuild refuse it before writing or building; when its runtime is present,
read views report the policy error as drift. `box edit` removes it. The help
and warning state space-VM root over every box, credential, native state,
declaration, controller/runtime state and writable host grant, without
OpenShell confinement; host share limits and read-only drives remain; turning
it off does not undo VM-wide changes or modules; while the box runs every space
operator can exec in, attach to, use, restart and recreate it and so gain that
authority, and only the host starts it; a closed session or lost holder ends
its process group.
[1](#source-1) [2](#source-2) [29](#source-29) [30](#source-30)

[FACT] The image recipe is the declared and profile features, then an internal
sudo installer for a privileged box. The installer requires no capabilities,
cannot be declared or listed and fetches no kernel material. It installs sudo
(apt when absent) and a root-owned 0440 drop-in for `sandbox`, checked by
`visudo` before it is moved into place, or fails the build naming the
privileged box; a test runs it against fake tools in a scratch root. Its
description and Dockerfile are hashed into the composed image, so changing
them is image drift for every privileged box. Image identity, builds and drift
use this recipe; composed policy is unchanged and staged for a confined
recreate. [2](#source-2) [3](#source-3) [22](#source-22) [31](#source-31)

[FACT] A privileged create runs `nerdctl run --privileged` (all capabilities,
host devices and new privileges in nerdctl 2.3.5) with the same namespaces,
resources, binds and child environment. PID 1 is the shim plus
`/opt/yoloctl/bin/yoloctl box-endpoint-internal` from a read-only directory
bind; preflight requires a root-owned chain and a static executable for the VM
architecture. Labels record the mode, launcher protocol and binary hash. [4](#source-4)
[5](#source-5) [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] The endpoint is dispatched before CLI, controller, Lima or operator setup.
It requires root PID 1, strict argv for its protocol, the workdir, a non-root
`sandbox` identity and a valid child environment, and listens last on a 0600
socket in an owner-only directory. [9](#source-9) [10](#source-10)

[FACT] It serves the yoloctl clients' SSH subset: no-auth for user `sandbox`
only, session channels, PTY requests with modes and measured pixels, resizes,
shell, exec, EOF, separate stderr without a PTY, exit status or RFC signal, and
keepalives. Every other request gets a negative reply. Connections, sessions,
commands, terminal names and modes are bounded. Children get the confined
launch's bash argv, workdir and HOME/USER/SHELL/PATH/TERM plus the injected map
without `OPENSHELL_*` keys, raw JSON or supervisor variables, and no sandbox
marker. [10](#source-10)
[11](#source-11)

[FACT] Each child leads its own session and process group with the mapped
credentials. The reaper observes exits without reaping and reaps a leader
once, after its group's cleanup; as PID 1 it also reaps orphans. Cleanup runs
once on channel close or leader exit: SIGHUP, then SIGKILL for remaining
members after a grace, while the unreaped leader reserves the group ID. A
session's admission slot stays charged until this cleanup and reaping end, so
a leader or descendant that cannot exit keeps it occupied even after SIGKILL.
A process that leaves the group
is ended only by container stop, which signals
the private PID namespace and escalates before the runtime's 30-second grace.
[10](#source-10)

[FACT] stdout and stderr share one post-exit drain deadline. On expiry the
sources close; EOF follows only if every copy then finishes, which does not
prove all output reached the client, and the genuine status is still sent when
the transport accepts it. Pinned x/crypto releases a write parked on the channel
window only when the peer answers close or the connection ends, so a peer that
withholds close or transport progress past a bound loses that whole SSH
connection; other connections, and a cooperative peer's siblings, continue.
[10](#source-10)

[FACT] Inspection classifies a container as privileged only when nerdctl's
recorded privileged setting, the endpoint argv, the exact read-only bind, the
mode label and a launcher label naming the argv's protocol agree; confined only
when none indicates privilege. A mode change, unknown evidence for a privileged
declaration or an older protocol is create-only drift in diff, show, inspect
and start. A proved
privileged runtime skips restart for policy-only changes and reports that
policy is not enforced. Only an explicit `box recreate` may switch modes; set,
edit, reconcile, rebuild and chat recreation refuse. Ports bypass the inner DNAT.
[12](#source-12) [13](#source-13) [14](#source-14) [15](#source-15) [16](#source-16)

[FACT] `space view` marks a declared privileged box, and an undeclared box whose
running container shows any privileged signal, with a warning pill that names
a confined, unknown or disagreeing runtime, and counts them on its title row.
An unmarked box is not a claim of confinement. [32](#source-32)

[FACT] Operator authority stops at the launch. Operators may declare the
setting, edit, set, copy, stop and destroy privileged boxes, and exec in, attach
to and use a running one. Each seam that creates or starts
a container (managed create, recreate and restart, and the shared start behind
box start, attach/use revival and chat) counts the launch as privileged by the
declaration and, for a start or restart, by any privileged or undecidable
signal of the existing container. Before the seam's teardown, image build or
start (a command may first do non-launch work, such as an explicit rebuild's
image build), an operator's privileged launch is refused unless it is a managed
restart/recreate of a running privileged launch on the current endpoint
protocol, or the start leg of an explicit restart bound to the container ID
observed running before its stop.
A stopped box, a confined-to-privileged recreate and a later request get no
continuation; the start re-reads the container. An operator create or copy that
would launch is refused before saving and names `--manifest-only`; a refusal
after a set, edit or port save says the declaration is saved. Host launches
print the operator-access warning. [15](#source-15) [17](#source-17)
[18](#source-18) [19](#source-19)

[FACT] The setting adds no KVM device, module or kernel. A privileged
container receives the VM's devices, and the bundled kernel recipe builds
`VIRTUALIZATION`, `KVM`, `KVM_INTEL` and `KVM_AMD` in. [3](#source-3)
[6](#source-6) [26](#source-26) [27](#source-27)

[FACT] Isolated tests drive the endpoint with the real holder, sshtransport and
readiness probe, unprivileged and outside PID 1; stop's namespace-wide signal is
not run there. leogr reported all nine stages of the disposable host gate
passing (2026-10-23), including sudo root, the held session, port forwarding
and root-descendant teardown on stop; that run checked the earlier host-only
operator denial, and the gate's operator phase now expects this launch boundary
but has not been run against it. The gate now selects `--privileged` and
first checks the retired-name refusal; that form has not been run. It checks
the module capability without loading a module and exercises no arbitrary
device.
[20](#source-20) [21](#source-21) [22](#source-22) [23](#source-23)
[24](#source-24) [25](#source-25)

[FACT] leogr reported a nested run (2026-10-24; AMD Ryzen 9 5900X, Arch Linux
7.2.11, `kvm_amd` nested=1). With this kernel the outer guest answered KVM API
12 and created a VM; a confined box saw no `/dev/kvm`. A privileged `go` box
built yoloctl and booted an inner Firecracker space; an inner box ran as the
mapped UID, wrote through its share and echoed through `box use`. After inner
stop/start, exec worked, but a scripted Enter was acknowledged and not executed
until a manual Enter. Inner teardown, an in-box `make check` and Intel hosts
were not run. [28](#source-28)

## Sources

1. <a id="source-1"></a> [internal/capabilities/capabilities.go](../../internal/capabilities/capabilities.go#L124-L132) — `RetiredPrivilegedCapability`, `ErrRetiredPrivileged`; `Lookup` at L338; custom-name refusals in `user.go` at L190 and L332.
2. <a id="source-2"></a> [internal/agentbox/privileged.go](../../internal/agentbox/privileged.go#L22-L44) — `PrivilegedAuthority`, `PrivilegedOperatorAccess` and `RuntimeFeatures`.
3. <a id="source-3"></a> [internal/features/privileged.go](../../internal/features/privileged.go#L5-L61) — internal installer and its identity note.
4. <a id="source-4"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L436-L487) — privileged launch; creation check at L263.
5. <a id="source-5"></a> [internal/container/container.go](../../internal/container/container.go#L86-L122) — run option; inspected record at L546.
6. <a id="source-6"></a> [nerdctl v2.3.5 run_security_linux.go](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/cmd/container/run_security_linux.go#L43-L48) — spec options; `dockercompat.go#L665` and `create.go#L670` record it and reserve its label.
7. <a id="source-7"></a> [internal/agentbox/privileged.go](../../internal/agentbox/privileged.go#L132-L183) — binary verification.
8. <a id="source-8"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L1824-L1830) — runtime preflight.
9. <a id="source-9"></a> [cmd/yoloctl/main.go](../../cmd/yoloctl/main.go#L35-L39) — early dispatch.
10. <a id="source-10"></a> [internal/boxendpoint/server_linux.go](../../internal/boxendpoint/server_linux.go#L89-L1174) — server, children, reaper, PID 1 and socket.
11. <a id="source-11"></a> [internal/boxendpoint/boxendpoint.go](../../internal/boxendpoint/boxendpoint.go#L55-L215) — limits, argv and environment.
12. <a id="source-12"></a> [internal/agentbox/privileged.go](../../internal/agentbox/privileged.go#L46-L130) — `ClassifyRuntime`.
13. <a id="source-13"></a> [internal/agentbox/diff.go](../../internal/agentbox/diff.go#L196-L248) — policy note and `runtimeModeReason`.
14. <a id="source-14"></a> [internal/agentbox/privileged.go](../../internal/agentbox/privileged.go#L185-L215) — explicit-recreate mode gate.
15. <a id="source-15"></a> [cmd/yoloctl/managed_applier.go](../../cmd/yoloctl/managed_applier.go#L35-L91) — launch guards before teardown and the shared recreate gate.
16. <a id="source-16"></a> [internal/port/port.go](../../internal/port/port.go#L40-L48) — direct routing; teardown at L182-L213.
17. <a id="source-17"></a> [cmd/yoloctl/privileged_operator.go](../../cmd/yoloctl/privileged_operator.go#L16-L211) — flag help, `guardPrivilegedLaunch`, `privilegedLaunchReason`, continuation, `authorizePrivilegedRestart`, create refusal, `savedDeclarationError` and `warnBoxAuthority`.
18. <a id="source-18"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L2033-L2040) — `startExistingAgentboxContainer` guard; explicit restart's start-leg context at L2242-L2262.
19. <a id="source-19"></a> [cmd/yoloctl/agentbox.go](../../cmd/yoloctl/agentbox.go#L1760-L1775) — manifest-only notice and create/copy refusal; saved-declaration reports in `agentbox_reconcile.go` at L728 and L782.
20. <a id="source-20"></a> [internal/boxendpoint/server_linux_test.go](../../internal/boxendpoint/server_linux_test.go#L1) — protocol tests.
21. <a id="source-21"></a> [internal/session/holder_endpoint_linux_test.go](../../internal/session/holder_endpoint_linux_test.go#L1) — holder against the endpoint.
22. <a id="source-22"></a> [internal/agentbox/privileged_test.go](../../internal/agentbox/privileged_test.go#L1) — setting-only resolution, retired-grant refusal without rewrite, setting round trip, installer contract, evidence and drift.
23. <a id="source-23"></a> [cmd/yoloctl/privileged_operator_test.go](../../cmd/yoloctl/privileged_operator_test.go#L1) — launch classification, operator boundary, applier ordering, restart continuation, create refusal, start seam and views.
24. <a id="source-24"></a> [scripts/smoke/dev-privileged.sh](../../scripts/smoke/dev-privileged.sh#L1) — opt-in disposable-host gate; operator phase at L154.
25. <a id="source-25"></a> [internal/boxendpoint/lifecycle_linux_test.go](../../internal/boxendpoint/lifecycle_linux_test.go#L1) — owned-group escalation, competing cleanup, blocked control writes and admission reuse.
26. <a id="source-26"></a> [assets/vm/firecracker/inputs.py](../../assets/vm/firecracker/inputs.py#L24-L35) — required built-in options.
27. <a id="source-27"></a> [assets/vm/firecracker/kernel.config](../../assets/vm/firecracker/kernel.config#L711-L718) — effective KVM options.
28. <a id="source-28"></a> [internal/firecracker/driver_linux.go](../../internal/firecracker/driver_linux.go#L238-L286) — `Driver.Start`: `/dev/kvm` open and `setsid` helper.
29. <a id="source-29"></a> [internal/agentbox/agentbox.go](../../internal/agentbox/agentbox.go#L155-L162) — `Config.Privileged`; retired-name validation at L580, run by `Save` at L641, `planReconcile` in `reconcile.go` at L118 and `runRebuild` in `cmd/yoloctl/agentbox_reconcile.go` at L163, which composes policy at L169 before its build (`box edit` hint at L679); policy errors as drift with a present runtime in `internal/boxstate/boxstate.go` at L474-L495; chat-server refusal in `chat.go` at L69; feature refusal in `compose.go` at L84.
30. <a id="source-30"></a> [cmd/yoloctl/agentbox_reconcile.go](../../cmd/yoloctl/agentbox_reconcile.go#L636-L641) — `box set --privileged`; create/copy flag in `agentbox.go` at L989 and copy inheritance in `agentbox_copy.go` at L306.
31. <a id="source-31"></a> [internal/features/privileged_test.go](../../internal/features/privileged_test.go#L1) — installer wording and its script run against fake package, account and sudo tools.
32. <a id="source-32"></a> [Live space view](space-view.md) — inventory launch evidence and the privilege pill rule.
