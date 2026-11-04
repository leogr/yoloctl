# Landlock status in box show

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `box show` runs in the space controller. Its kernel probe issues the
documented `landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)`
query in that native process, outside workload seccomp. Outside the controller,
or for another space, the result is unknown. Securityfs is not read. The
Firecracker guest does not mount it, but it refuses to boot below ABI 3.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Only ENOSYS (not supported by the kernel) and EOPNOTSUPP (disabled at boot) are reported
as `kernel-unsupported` and `kernel-disabled`; both print `DEGRADED`. Other errno
values and impossible versions are `unknown` with a probe error. [1](#source-1)
[4](#source-4) [6](#source-6)

[FACT] The box's retained `nerdctl logs` output is streamed, keeping at most 4 KiB per line from each
stream. The first Landlock line is a hint; the first line matching OpenShell
v0.0.72 failure wording (`unavailable`, `restrict_self failed`,
`enforce-failed`, `without filesystem restrictions`) yields
`sandbox-reported-failure`. That is labelled retained-log evidence, not proof
about current workloads. A kernel gap outranks it. An unreadable log turns
`available` into `unknown`. [1](#source-1) [5](#source-5) [6](#source-6)

[FACT] A supported kernel with no reported failure is `available (VM kernel ABI
N)` and states that per-process enforcement is not verified. Text and JSON
(`summary`, `kernel_supported`, `abi`, `probe_error`, `log_hint`) carry the
same verdict; privileged boxes stay `not applied`/`not-applied`. [1](#source-1)
[7](#source-7) [6](#source-6)

## Sources

1. <a id="source-1"></a> [Landlock probe](../../cmd/yoloctl/landlock.go#L60-L267) — `classifyLandlockABI`, `probeVMLandlockKernel`, `inspectLandlock`, `landlockVerdict`, `landlockLogFailureMarkers`, `scanOpenshellLandlockLog`, `landlockSummaryLine`.
2. <a id="source-2"></a> [Linux query](../../cmd/yoloctl/landlock_linux.go#L7) — `queryLandlockABI`.
3. <a id="source-3"></a> [Guest boot](../../internal/vmguest/init_linux.go#L154-L199) — mounted filesystems and the ABI >= 3 guard.
4. <a id="source-4"></a> [Kernel Landlock ABI](https://docs.kernel.org/userspace-api/landlock.html#landlock-abi-versions) — version query and ENOSYS/EOPNOTSUPP meanings.
5. <a id="source-5"></a> [OpenShell v0.0.72 Landlock events](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-process/src/sandbox/linux/landlock.rs#L120-L310) — unavailable and `restrict_self` failure events; readiness in `mod.rs` `log_sandbox_readiness`.
6. <a id="source-6"></a> [Regressions](../../cmd/yoloctl/landlock_test.go#L16-L241) — summary text, errno classification, outside-controller probe, verdict precedence, log scan and text/JSON agreement.
7. <a id="source-7"></a> [Show rendering](../../cmd/yoloctl/agentbox.go#L384-L389) — text line; JSON `landlockJSON` and privileged `not-applied` follow `writeAgentboxShowJSON`.
