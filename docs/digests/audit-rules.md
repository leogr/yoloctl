# Audit rule paths and container recognition

Verification: `3e04d2b43ab4c848462358f2cc74cc1845328f5f` and accompanying changes.

[FACT] Filesystem rules distinguish the path changed by a syscall from the
payload of a new symbolic link. In pinned libs 0.26.1, `fs.path.source` for
symlink/symlinkat is the new link location; `fs.path.target` is its destination
text. An image layer's link under containerd/BuildKit storage pointing at
`/usr/lib/systemd/...` does not modify that VM path. Both persistence and
infrastructure rules therefore exclude symbolic-link payloads from target
matching, while checking the created link's source path. Rename/hard-link
targets remain covered. [1](#source-1) [2](#source-2)

[FACT] The exception is based on syscall semantics, with no blanket exemption
for containerd, buildkitd, nerdctl or containerd-shim. Real protected name/source
paths remain eligible regardless of which runtime process touches them; this
includes VM units, cron, SSH keys, loader configuration and infrastructure.
The existing host package/controller exceptions remain. Known OpenShell setup
infrastructure writes use the same fixed-executable, schema-1 confined-container
and protocol-2 context as the namespace rule. Missing context does not grant
that exception. These remain detection heuristics, not authenticity proofs.
[1](#source-1) [3](#source-3)

[FACT] Process inspection additionally excludes only successful
`process_vm_readv` events from `/.openshell/runtime/openshell-sandbox` with
`proc.name=openshell-netwo`, schema 1, protocol 2, confined runtime mode and
`network_unconfined=false`. Failed reads, ptrace, writes, startup thread names,
other executable paths, privileged mode and unknown/missing context gain no
exception. Existing debugger and host-scope rules are unchanged. A renamed
ordinary executable still remains eligible. The predicate does not verify
the target PID/address or executable authenticity: a substituted or compromised
runtime matching this context can hide successful reads. Predicate fixtures
do not prove live event attribution or noise reduction. Explicit updated
`audit enable` installs changed pinned rules; there is no automatic reload.
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
This selects descendants, including possible setup children; it does not prove
which confinement stage a process reached. Only **UID set to root** (successful
setuid/setreuid/setresuid requesting a zero UID, including saved UID) and
**audit escape test** (`yolo-esc-test`) ship enabled. An already-root no-op can
match; capabilities, setfsuid and GID transitions are outside scope. The control
checks selection only, never the escape predicates. [1](#source-1) [12](#source-12)

[FACT] Six candidates ship disabled: direct PATH-child mutation/write-open,
broader protected-prefix writes, connects outside the inner address, root exec,
host-classified exec and device opens. Writable directory mounts can permit
prefix writes; writable single-file binds can permit direct PATH-child writes.
The network predicate requires an explicit false network-unconfined label and a
known client tuple; it excludes loopback and the inner `10.200.0.2` address.
`EINPROGRESS` means an attempt in progress, not completed egress. A bypass
retaining that inner address and unconnected UDP are not detected. Device
exclusions include PTY and descriptor aliases. Host attribution alone cannot
establish escape. These rules remain disabled pending separate qualification.
[1](#source-1) [12](#source-12) [13](#source-13) [14](#source-14)

[FACT] The shared label predicate excludes privileged runtime mode and absent
labels. Unconfined-network descendants remain eligible for the enabled
UID/control rules. Labels and ancestry remain selection heuristics; the rules
provide alerts without remediation or tamper-proof guarantees.
Existing rules and the broker exception are byte-identical. Structural tests
and pinned engine compilation do not prove live selection or low noise.
[1](#source-1) [12](#source-12)

## Sources

1. <a id="source-1"></a> [Pinned rules](../../internal/audit/rules.yaml#L1) — `yolo_fs_target_mutation`, persistence/infrastructure and setup context.
2. <a id="source-2"></a> [libs 0.26.1 filesystem fields](https://github.com/falcosecurity/libs/blob/0.26.1/userspace/libsinsp/sinsp_filtercheck_fspath.cpp) — `create_fspath_checks`, symlink linkpath/target and rename/hard-link mappings.
3. <a id="source-3"></a> [Rule regressions](../../internal/audit/rules_test.go#L1) — privacy boundaries and `TestProtectedPathsIgnoreOnlySymlinkPayloads` structural checks.
4. <a id="source-4"></a> [Configuration](../../internal/audit/config.go#L1) — `Configuration`, enabled engines and explicit socket paths.
5. <a id="source-5"></a> [Container 0.7.6 CRI matcher](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/src/matchers/cri.cpp) — nerdctl scope pattern; native-containerd matching is separate.
6. <a id="source-6"></a> [Container 0.7.6 matcher selection](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/src/matchers/matcher.cpp) — engines select matchers; unmatched cgroups become host.
7. <a id="source-7"></a> [Container 0.7.6 metadata fetcher](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/go-worker/pkg/container/fetcher.go) — enabled-provider lookup and retry bounds.
8. <a id="source-8"></a> [Metadata extraction](../../third_party/audit-plugin/plugin.cpp#L1) — thread container ID lookup and bounded snapshot extraction.
9. <a id="source-9"></a> [Broker-read regressions](../../internal/audit/process_inspection_test.go#L1) — `TestOpenShellBrokerReadException`, positive/negative equality fixtures and unchanged inspection scope; not a Falco evaluator.
10. <a id="source-10"></a> [Native audit handlers](../../cmd/yoloctl/controller_audit_linux.go#L1) — explicit enable installs the pinned `RulesDigest`.
11. <a id="source-11"></a> [Configuration](../../internal/audit/config.go#L1) — automatic file reloads disabled.
12. <a id="source-12"></a> [Escape regressions](../../internal/audit/escape_rules_test.go#L1) — enablement, selectors, original-rule hash, PATH parity and network/device boundaries.
13. <a id="source-13"></a> [Composer](../../internal/agentbox/compose.go#L200) — extra mounts, `pathDirs`, `lintPathOverlap`.
14. <a id="source-14"></a> [PTY capability](../../internal/capabilities/capabilities.go#L1) — permitted PTY aliases.
