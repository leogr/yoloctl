# Audit rule paths and container recognition

Verification: `351da19df8eb0c537435095db835d13678bc5372` and accompanying changes.

[FACT] Filesystem rules distinguish the path changed by a syscall from the
payload of a new symbolic link. In libs 0.26.1, `fs.path.source` for
symlink/symlinkat is the new link location; `fs.path.target` is its destination
text. An image layer's link under containerd/BuildKit storage pointing at
`/usr/lib/systemd/...` does not modify that path. Rules exclude link payloads from target
matching while checking the created source. Rename/hard-link
targets remain covered. [1](#source-1) [2](#source-2)

[FACT] Syscall semantics give no blanket exemption
for containerd, buildkitd, nerdctl or containerd-shim. Protected name/source paths remain eligible, including units, cron, SSH keys,
loader configuration and infrastructure.
The existing host package/controller exceptions remain. Known OpenShell setup
infrastructure writes use the same fixed-executable, schema-1 confined-container
and protocol-2 context as the namespace rule. Missing context does not grant
that exception. These are heuristics, not authenticity proofs.
[1](#source-1) [3](#source-3)

[FACT] Process inspection excludes successful `process_vm_readv` from exactly
`/.openshell/runtime/openshell-sandbox`, with helper parent
`/opt/yoloctl/openshell-helper/yoloctl-openshell-helper`, name `openshell-netwo`
or `openshell-sandb`, schema 1, protocol 2, confined runtime and explicit
`network_unconfined=false/true`. Both modes retain the named broker's file-open
mediation: `ProviderFiles` copies pathname bytes through `task_memory::read_exact`.
Main-thread startup qualification also reads child memory/socket arguments before
either mode starts. Unmediated networking does not rename the broker.
[17](#source-17) [18](#source-18) [19](#source-19) [20](#source-20) [21](#source-21)

[FACT] Failed reads, ptrace, writes, other names/paths/parents, privileged mode
and missing/unknown context gain no exception. Debugger and host-scope rules
stay unchanged. Target PID/address and executable authenticity are not verified:
a compromised matching runtime can hide successful reads. Fixtures do not prove
live attribution/noise reduction; observed main-thread reads alone do not prove
file mediation. Updated `audit enable` installs changed rules; no automatic reload.
[1](#source-1) [9](#source-9) [10](#source-10) [11](#source-11)

[FACT] Container plugin 0.7.6 registers nerdctl systemd-scope recognition in its
CRI matcher; its native-containerd matcher handles namespace/ID layouts.
Configuration enables both engines against only `/run/containerd/containerd.sock`,
with the existing two-second per-engine timeout. Docker/Podman/other engines
stay disabled. The native API still enumerates containerd namespaces, including
default; the guest does not need its CRI service enabled. CRI RPCs may fail on
that socket; the worker fetcher tries enabled metadata providers until one
succeeds. No containerd configuration, runtime cgroup layout or yolo plugin ABI
is changed. [4](#source-4) [5](#source-5) [6](#source-6) [7](#source-7)

[FACT] The native-containerd matcher has no nerdctl systemd-scope pattern;
unmatched thread cgroups are reported as host before snapshot enrichment.
The C++ plugin joins a fresh, unambiguous snapshot only when the thread has a
recognized container ID. These source contracts require native event
qualification; a no-driver engine or structural rule test does not prove live
cgroup identity or noise rates. [5](#source-5) [6](#source-6) [8](#source-8)

## Escape candidates

[FACT] Seven added rules use schema-1/protocol-2 confined-container labels and
OpenShell runtime ancestry, excluding the runtime executables and fixed helper.
The eighth, disabled host-classification candidate, uses ancestry without labels.
Ancestry includes setup children; it does not prove confinement. Only **UID set to root** (successful
setuid/setreuid/setresuid requesting zero, including saved UID) and
**audit escape test** (`yolo-esc-test`) ship enabled. An already-root no-op can
match; capabilities, setfsuid and GIDs are outside scope. The condition uses
`evt.args contains "uid=0("`: the pinned formatter retains the decimal UID
before its resolved name, covering uid/ruid/euid/suid. Resolved `evt.arg.*`
contains names/placeholders. A crafted name containing `uid=0(` can also match
a nonzero UID: this accepted heuristic can produce an extra alert. Aggregate
arguments are never persisted. The control tests selection only.
[1](#source-1) [12](#source-12) [15](#source-15) [16](#source-16)

[FACT] Six candidates ship disabled: direct PATH-child mutation/write-open,
broader protected-prefix writes, connects outside the inner address, root exec,
host-classified exec and device opens. Writable directory mounts can permit
prefix writes; writable single-file binds can permit direct PATH-child writes.
The network predicate requires an explicit false network-unconfined label and a
known client tuple; it excludes loopback and the inner `10.200.0.2` address.
`EINPROGRESS` means an attempt in progress, not completed egress. A bypass
retaining that inner address and unconnected UDP are not detected. Device
exclusions include PTY and descriptor aliases. Host attribution alone cannot
establish escape. [1](#source-1) [12](#source-12) [13](#source-13) [14](#source-14)

[FACT] The shared label predicate excludes privileged runtime mode and absent
labels. Unconfined-network descendants remain eligible for the enabled
UID/control rules. Labels and ancestry remain selection heuristics; the rules
provide alerts without remediation or tamper-proof guarantees.
Escape candidates retain their existing enablement and selectors. Compilation and fixtures
do not prove live selection or noise rates.
[1](#source-1) [12](#source-12)

## Sources

1. <a id="source-1"></a> [Pinned rules](../../internal/audit/rules.yaml#L1) — `yolo_fs_target_mutation`, persistence/infrastructure and setup context.
2. <a id="source-2"></a> [libs 0.26.1 filesystem fields](https://github.com/falcosecurity/libs/blob/0.26.1/userspace/libsinsp/sinsp_filtercheck_fspath.cpp) — `create_fspath_checks`.
3. <a id="source-3"></a> [Rule regressions](../../internal/audit/rules_test.go#L1) — `TestProtectedPathsIgnoreOnlySymlinkPayloads`.
4. <a id="source-4"></a> [Configuration](../../internal/audit/config.go#L1) — `Configuration`.
5. <a id="source-5"></a> [Container 0.7.6 CRI matcher](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/src/matchers/cri.cpp) — nerdctl scope recognition.
6. <a id="source-6"></a> [Container 0.7.6 matcher selection](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/src/matchers/matcher.cpp).
7. <a id="source-7"></a> [Container 0.7.6 metadata fetcher](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/go-worker/pkg/container/fetcher.go).
8. <a id="source-8"></a> [Metadata extraction](../../third_party/audit-plugin/plugin.cpp#L1).
9. <a id="source-9"></a> [Broker-read regressions](../../internal/audit/process_inspection_test.go#L1) — known modes/unknown context; not a Falco evaluator.
10. <a id="source-10"></a> [Native audit handlers](../../cmd/yoloctl/controller_audit_linux.go#L1) — `enable` deployment.
11. <a id="source-11"></a> [Configuration](../../internal/audit/config.go#L1) — automatic file reloads disabled.
12. <a id="source-12"></a> [Escape regressions](../../internal/audit/escape_rules_test.go#L1) — selectors, enablement and rule hash.
13. <a id="source-13"></a> [Composer](../../internal/agentbox/compose.go#L200) — `lintPathOverlap`.
14. <a id="source-14"></a> [PTY capability](../../internal/capabilities/capabilities.go#L1) — permitted PTY aliases.
15. <a id="source-15"></a> [libs 0.26.1 event extraction](https://github.com/falcosecurity/libs/blob/0.26.1/userspace/libsinsp/sinsp_filtercheck_event.cpp#L1070) — `TYPE_ARGS`/`TYPE_ARGSTR`.
16. <a id="source-16"></a> [libs 0.26.1 UID formatting](https://github.com/falcosecurity/libs/blob/0.26.1/userspace/libsinsp/event.cpp#L1164) — `PT_UID`.

17. <a id="source-17"></a> [Unmediated mode](../../third_party/openshell/patches/0001-unmediated-network.patch#L167) — file-open notifications retained.
18. <a id="source-18"></a> [Pinned broker](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/network_broker.rs#L586) — dispatch; thread L292.
19. <a id="source-19"></a> [Pinned file handler](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/provider_files.rs#L65).
20. <a id="source-20"></a> [Pinned task-memory read](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/linux/task_memory.rs#L48) — readv.

21. <a id="source-21"></a> [Pinned startup qualification](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-sandbox/src/main.rs#L112) — `qualify_runtime`, socket reads L656/854, `run_boundary` L1761; patch0001 retains both modes.
