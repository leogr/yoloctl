# VM-native Falco auditing

Verification: `3933d13318aec32653d72f806327b18291bc4a18` and accompanying changes.

[FACT] Auditing is opt-in and initially disabled. The host's `audit` command
opens a read-only view; `status`, `enable`, `disable`, `logs` and `export` use
the running matching controller. They never boot or recreate a VM. Operators
have neither the registered frontend surface nor server authority for audit
operations. Opening or filtering the view does not install or enable auditing.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Desired enablement, tool version and rules digest belong to native space
settings. Tools/config/logs live in the root-only `audit` directory on the
independent data disk, separate from account leaves and box binds. Runtime
health lives under the controller's volatile run root. Native data survives
space recreation; space backup omits audit logs/tools and audit enablement,
and import clears enablement. [4](#source-4) [5](#source-5) [6](#source-6)

[FACT] Explicit enable downloads the architecture-specific official Falco
0.45.2 tarball into an owned host cache, checks its fixed size/SHA256, and
transfers it through the controller. Native installation selects only Falco
and the bundled container plugin into an immutable private version directory;
archive and selected-file digests/modes are checked before reuse/execution.
No package scripts, falcoctl, floating rules updates or external event sinks
are installed. [7](#source-7) [8](#source-8)

[FACT] The fixed `yoloctl-audit` root service runs outside boxes: Lima uses
systemd with control-group teardown and a ten-second stop timeout; Firecracker
uses PID-1 owned cgroups/reaping with exact launcher/environment validation,
without automatic restart. The collector
checks kernel BTF, validates its pinned rule/config and performs a bounded real
modern-eBPF capture before reporting loaded health. There is no kmod fallback.
The controller applies certain enabled desired state after verified startup;
a reserved/uncertain audit operation prevents that reconciliation. Disable
requires acknowledged service stop and retains logs/tools. Falco failure does
not stop workloads; control uncertainty uses a separate audit receipt domain.
The controller's ordinary global storage-uncertainty rules still apply. The
collector captures the first eight stderr lines/up to 4 KiB while draining the
rest; failed health retains the phase/exit or timeout. Host status escapes controls and
caps the final diagnostic at 1024 bytes. Event stdout never enters this
diagnostic; it is absent from log/export records. Non-Falco failures retain
their own cause instead of a blanket kernel explanation.
[2](#source-2) [9](#source-9) [10](#source-10) [11](#source-11) [19](#source-19)

[FACT] Firecracker audit first makes a bounded read-only PID-1 support query;
old images are refused before toggle effects, with an explicit
`space recreate NAME --update-vm-image` hint. Audit commands never replace
media. The bundled kernel includes BTF/JIT/tracing; this is not live load proof.
[19](#source-19) [20](#source-20) [21](#source-21)

[FACT] Pinned rules cover runtime/admin socket access,
infrastructure changes, workload BPF/modules, namespace changes, cross-process
inspection, selected protected-file denials and persistence paths. The noisy
anonymous/temporary executable rule is disabled. `yolo-audit-test` remains the
controlled acceptance signal. Results distinguish success/refusal; they do not
identify a policy mechanism. Fixed OpenShell paths and explicit context narrow
setup/debugger exceptions; host controller/package exceptions cover expected
maintenance. These are detection heuristics, not executable authenticity proofs.
[2](#source-2) [16](#source-16)

[FACT] The bundled container plugin 0.7.6 uses only the system containerd socket,
with a two-second engine timeout; its pinned implementation enumerates namespaces,
including native nerdctl's default namespace, without CRI. New containers carry
schema-1 observation labels with explicit account/network/runtime defaults;
legacy reconcile labels retain their omitted-default semantics. The C++ plugin
joins a bounded private controller snapshot to event container IDs and exports
only selected `yolo.*` fields. Missing/stale/ambiguous metadata stays unavailable,
never discards the event and never falls back after a false plugin availability
field. Context is not per-process confinement proof. Native identity/hits/noise
still need host qualification. [12](#source-12) [13](#source-13)
[15](#source-15) [17](#source-17) [18](#source-18)

[FACT] The collector persists bounded, selected process/user/container/path/result
fields and a rendering of the pinned rule template. Arbitrary raw output,
argv, environment and buffers remain excluded. Unknown metadata stays unavailable;
[output and follow contracts](audit-output.md) define JSON, pretty/TUI display,
colour and quiet polling. Terminal controls are escaped.
Collector drops, driver drops (unknown until reported), metadata gaps and
retention gaps are visible. This is best-effort detection, without remediation
or a complete, VM-root-tamper-proof trail. [12](#source-12) [13](#source-13) [22](#source-22)

[FACT] Event segments rotate at 64 MiB total. Reads exclude events older than
seven days; minutely collector/controller maintenance expires them while
VM infrastructure runs. Controller maintenance requires a confirmed inactive or
failed collector, regardless of desired enablement, and retains native locking
and receipt holds. Viewers own independent bounded reads and cannot block
the collector. Export fixes an upper cursor/time, applies interval/box filters,
reports gaps/counts, and publishes privacy-filtered JSONL only after a complete
controller result. The private host file is fsynced and linked without replacing
an existing file; interrupted/invalid exports never publish a partial result.
Export also works with auditing disabled while the controller is running.
[1](#source-1) [2](#source-2) [14](#source-14)

## Sources

1. <a id="source-1"></a> [Host commands](../../cmd/yoloctl/audit.go#L1) — `newAuditCmd`, `scanAudit`, `publishAuditExport`.
2. <a id="source-2"></a> [Native handlers](../../cmd/yoloctl/controller_audit_linux.go#L1) — authorization, toggle, health, reconciliation and retention.
3. <a id="source-3"></a> [Read-only view](../../internal/auditui/view.go#L1) — `Model`, bounded requests, filters, pause and details.
4. <a id="source-4"></a> [Native settings](../../internal/spaceconfig/native.go#L1) — `loadNative`, `saveNative`.
5. <a id="source-5"></a> [Private paths](../../internal/layout/audit.go#L1) — native disk, run root and host cache selection.
6. <a id="source-6"></a> [Archive selection](../../internal/spacearchive/state.go#L74) — `WriteState` excludes audit infrastructure/enablement.
7. <a id="source-7"></a> [Pinned artifacts](../../internal/audit/artifacts.go#L1) — `Pinned`, `Fetch`, `Install`, `VerifyInstalled`.
8. <a id="source-8"></a> [Falco 0.45.2 release](https://github.com/falcosecurity/falco/releases/tag/0.45.2) — official architecture tarballs.
9. <a id="source-9"></a> [Guest service adapter](../../internal/vmservice/service.go#L58) — `Start`, `StopAcknowledged`.
10. <a id="source-10"></a> [Collector](../../internal/audit/collector_linux.go#L1) — modern-eBPF qualification, owned child and health.
11. <a id="source-11"></a> [Audit holds](../../internal/controller/holds.go#L1) — `CheckAuditReconcile`, independent resource conflicts.
12. <a id="source-12"></a> [Pinned config/rule](../../internal/audit/config.go#L1) — `Configuration`, `Rules`, `ServiceConfig`.
13. <a id="source-13"></a> [Event privacy](../../internal/audit/types.go#L1) — `ParseEvent`, `Clean`, explicit availability and counters.
14. <a id="source-14"></a> [Log storage](../../internal/audit/logs.go#L1) — `Scan`, `Append`, `Prune`, finite cursors and bounds.

15. <a id="source-15"></a> [Observation labels](../../internal/agentbox/labels.go#L1) — `EncodeLabels`, `DecodeLabels`; explicit audit defaults do not change drift fields.
16. <a id="source-16"></a> [Pinned rules](../../internal/audit/rules.yaml#L1) — signal families, selected outputs and narrow actor exceptions.
17. <a id="source-17"></a> [Container 0.7.6 implementation](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/go-worker/pkg/container/containerd.go) — `get`, `getFullContainersInfos`, namespace enumeration and bounded lookups.

18. <a id="source-18"></a> [Metadata extraction](audit-metadata.md) — C ABI, generation/freshness/identity bounds, packaging and limits.

19. <a id="source-19"></a> [Fixed PID-1 audit unit](../../internal/vmguest/units_linux.go#L39) — `validateUnit`, `manageUnit`; closed launch/query regressions in audit_unit_linux_test.go.
20. <a id="source-20"></a> [Firecracker support query](../../internal/vmservice/service.go#L170) — `AuditSupported`; old-image pre-effect regression in cmd/yoloctl/audit_image_linux_test.go.
21. <a id="source-21"></a> [Kernel recipe](../../assets/vm/firecracker/inputs.py#L38) — `KERNEL_EXTRAS`, `publish_kernel`; cross-host ELF/config regression in assets/audit_kernel_test.go.

22. <a id="source-22"></a> [Selected rendering](../../internal/audit/output.go#L1) — `SelectedFields`, `RenderOutput`.
