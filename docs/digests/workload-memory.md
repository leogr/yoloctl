# Shared workload memory

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] The guest workload pool leaves the larger of 1 GiB or one eighth of
usable guest RAM outside its hard limit. Explicit VMs below 2 GiB instead leave
half outside. Limits round down to whole MiB; pool swap is disabled.
`memory.high=max` disables early throttling while `memory.max` retains the hard
cap. Linux's high limit applies reclaim/throttling without invoking OOM; this
pool has no external pressure manager to relieve it. Headroom is not reserved
or guaranteed capacity for infrastructure. [1](#source-1) [15](#source-15)

[FACT] Firecracker init enables cgroup v2 controllers and creates
`/yoloctl-workloads` before runtime startup. Boxes use cgroupfs children;
BuildKit starts in a separate leaf and its containerd worker creates build-step
children below the pool. Lima deploys `yoloctl-workloads.slice`, puts BuildKit's
service there and uses systemd scopes for boxes and build steps. The worker's
parent and `SystemdCgroup` option are configured together. Controller, session
holders, SSH and containerd are not moved into the pool. [1](#source-1)
[2](#source-2) [3](#source-3) [4](#source-4) [5](#source-5)

[FACT] New boxes default to `--memory shared`: no additional per-box hard cap.
Empty/zero memory has the same meaning; an explicit size such as `2g` adds a
child limit. New boxes likewise omit a CPU cap: no `--cpus` is passed to the
runtime, which adds no box CPU bandwidth limit but is not unlimited host capacity
or guaranteed fairness. Explicit positive or fractional `--cpus` values add a
cap and `0` removes one. Existing declarations and copies without an override
keep their saved limits. Actual inspect byte limits and CPU quota/period are normalized for
reconcile, including removing limits; equivalent units do not cause recreation.
Managed creation stamps pool version 1. Containers predating that label require
recreation to join the pool; they are not moved live. [5](#source-5)
[6](#source-6) [7](#source-7) [8](#source-8) [16](#source-16)

[FACT] Managed creation/builds verify the actual pool limit files against usable
guest RAM. Recreation checks before session teardown as well. Missing or
inconsistent protection refuses new workloads with an update hint; read/stop
commands remain available. An older Firecracker image needs explicit image
replacement, not just a controller update. Lima deployment publishes the slice,
BuildKit drop-in and TOML before activation; failure restores previous files
before restarting. The acknowledgment hash covers all three files.
[4](#source-4) [5](#source-5) [9](#source-9)

[FACT] Firecracker BuildKit exit no longer powers off the VM. Requests share
bounded startup/readiness; after daemon exit the next explicit request may start
a new daemon. No interrupted build is automatically replayed. Lima configures
service restart on failure; new builds wait for service readiness first.
`box inspect` exposes observed box limits and
aggregate pool current/peak bytes and OOM counters, or explicitly unavailable
counters. JSON `high_bytes: null` denotes disabled early throttling. Counts
span the pool's lifetime and are not attributed to that box.
[1](#source-1) [3](#source-3) [5](#source-5) [10](#source-10) [11](#source-11)

[FACT] Without the `memory_localevents` mount option, `memory.events` fields
are hierarchical, so the pool's `oom_kill` can include kills under a box's own
limit; it does not name the limit that was reached. Space view shows these
counters beside box charges; see [space view memory](space-view-memory.md).
[15](#source-15) [17](#source-17)

[FACT] Unit tests cover budgets, malformed limits, configuration preservation,
deployment rollback, missing protection, resource normalization and builder
death/readiness/concurrency. The isolated memory smoke exercises a 2304 MiB
allocation, prompt child/pool OOM with a surviving peer, limit removal,
daemon/RUN placement, and builder restart. OOM probes have a fatal guest alarm
and a host timeout; failures collect bounded pressure/event diagnostics without
repeating the allocation. These checks require a VM of at least 4 GiB;
unit tests alone do not validate either backend's kernel enforcement.
[12](#source-12) [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [resources/workloads.go](../../internal/resources/workloads.go#L29) — budgets, slice, BuildKit worker and kernel-limit probe.
2. <a id="source-2"></a> [resources/workloads_linux.go](../../internal/resources/workloads_linux.go#L16) — dedicated guest cgroup initialization.
3. <a id="source-3"></a> [vmguest/buildkit_linux.go](../../internal/vmguest/buildkit_linux.go#L77) — daemon leaf, lazy startup and exit handling.
4. <a id="source-4"></a> [buildkit_gc.go](../../cmd/yoloctl/buildkit_gc.go#L17) — systemd deployment, activation hash and rollback.
5. <a id="source-5"></a> [container/container.go](../../internal/container/container.go#L75) — managed run placement, pool preflight and resource inspection.
6. <a id="source-6"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L985) — create CPU/memory defaults.
7. <a id="source-7"></a> [agentbox/diff.go](../../internal/agentbox/diff.go#L213) — normalized limits and pool-version drift.
8. <a id="source-8"></a> [agentbox/live.go](../../internal/agentbox/live.go#L338) — create-time pool membership label.
9. <a id="source-9"></a> [managed_applier.go](../../cmd/yoloctl/managed_applier.go#L36) — preflight before session teardown.
10. <a id="source-10"></a> [container/memory.go](../../internal/container/memory.go#L16) — bounded aggregate counter reads.
11. <a id="source-11"></a> [agentbox_inspect.go](../../cmd/yoloctl/agentbox_inspect.go#L436) — JSON and human diagnostics.
12. <a id="source-12"></a> [resources/workloads_test.go](../../internal/resources/workloads_test.go#L11) — budgets, worker configuration and units.
13. <a id="source-13"></a> [vmguest/buildkit_linux_test.go](../../internal/vmguest/buildkit_linux_test.go#L14) — process recovery and bounded readiness.
14. <a id="source-14"></a> [smoke/memory.sh](../../scripts/smoke/memory.sh#L1) — isolated runtime gate.
15. <a id="source-15"></a> [Linux cgroup v2 memory interface](https://docs.kernel.org/admin-guide/cgroup-v2.html#memory-interface-files) — throttle versus hard limit, checked against Linux 7.2.11 documentation/source; hierarchical `memory.events`, read at docs.kernel.org 7.3.0 on 2026-10-26.
16. <a id="source-16"></a> [agentbox_copy.go](../../cmd/yoloctl/agentbox_copy.go#L310) — copy keeps the source CPU value without an override.
17. <a id="source-17"></a> [Space view memory readings](space-view-memory.md) — where pool and box counters are shown.
