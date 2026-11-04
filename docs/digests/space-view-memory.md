# Space view memory readings

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] Each sample reads VM `MemTotal`/`MemAvailable` and the workload pool's
`memory.current`, `memory.max`, optional `memory.peak` and `oom_kill`. Each
running box contributes `memory.current`, `memory.max` and the `anon`, `file`,
`shmem` and `inactive_file` keys of `memory.stat`, all through the verified
pool-child cgroup and bounded no-follow reads. An unread or malformed limit
or breakdown is named in `unavailable` (`mem_max`, `mem_stat`) and leaves a
valid charge intact. An absent stat key is nil, not zero.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Kernel semantics: `memory.current` covers a cgroup and its
descendants. `file` includes tmpfs and shared memory. `inactive_file` belongs
to the reclaim LRU lists, which need not match the type counters; shmem sits
on anon lists. `memory.events` fields are hierarchical unless the mount uses
`memory_localevents`, and `oom_kill` counts this cgroup's processes killed by
any OOM killer. [4](#source-4)

[FACT] Box memory comes only from the current sample, gated by the same
live/unpaused/recent-sample rule as activity. It is instantaneous and needs no
second sample. A stopped runtime, missing or failed box sample, or a stale,
paused or lost stream has no reading: cards, compact rows and details show
the reason instead of the last value. The memory sort orders current raw
totals first, then the boxes without one, stably by name.
[5](#source-5) [6](#source-6)

[FACT] Each running box's inventory entry carries its observed container ID,
and a sample counts only when it names that same container. The stream sends
a refreshed inventory before its next sample, so a replaced box shows no
reading until its new container's first sample. A box whose ID is unknown,
because the runtime lookup failed, has no reading. CPU rates keep their own
rule. [3](#source-3) [5](#source-5) [13](#source-13)

[FACT] The estimated working set is `current − min(current, inactive_file)`
from one sample. It is labeled with `~` and "work"/"working", or explained in
the details as an estimate, never as non-reclaimable memory. Without a
breakdown there is no estimate. [5](#source-5)

[FACT] A card's bar spans the raw charge over its scale: the box's own cap,
else the pool limit, else VM RAM. Every label beside a drawn bar names that
scale ("of 2G cap" or "1.9G/2G cap", "pool", "VM"). An unread limit draws no
scale and says "limit unknown". The estimate takes the box's accent, the
excluded inactive file pages are muted, and the bar never uses heat colors.
Narrower cards drop words before shrinking the bar below six cells. A session
that exited while its container runs keeps its current CPU and memory; when
it fits, the stats row says `session exited · container up`.
[5](#source-5) [7](#source-7)

[FACT] Clicking a card's or compact row's memory opens live details: total
with bytes, limit or shared pool scope, estimate and formula, and the
`memory.stat` keys listed separately without sums. Compact rows show only a
current raw total. [5](#source-5) [7](#source-7)

[FACT] The VM row fills only `MemTotal − MemAvailable` over `MemTotal`, the
same numbers as its `used` label. The pool's charge is shown beside it,
labeled as including cache, with peak and OOM count. An unreadable meminfo is
"VM memory unavailable". A stale stream shows no current reading. The VM
details mark a stale last sample and explain that charges need not add up to
VM used. The OOM toast names "a box or build" and the pool count.
[7](#source-7) [8](#source-8) [9](#source-9)

[FACT] Tests cover parsing, unread counters, the estimate's edge cases,
fill-versus-label, stale/errored/stopped/replaced/first samples, a replaced
or unknown container before its sample, sort order, heat-free near-cap bars,
labels at every width, details and clicks, and the encoded inventory ID and
breakdown from fixture cgroup files. They use fixtures, not a live VM.
[10](#source-10) [11](#source-11) [12](#source-12) [14](#source-14)

## Sources

1. <a id="source-1"></a> [collect.go](../../internal/spacetelemetry/collect.go#L175-L232) — `box`: charge, limit, `memory.stat` and named unavailability.
2. <a id="source-2"></a> [parse.go](../../internal/spacetelemetry/parse.go#L127-L141) — `ParseMemStat`.
3. <a id="source-3"></a> [telemetry.go](../../internal/spacetelemetry/telemetry.go#L170-L216) — `BoxSample`, signals and `MemStat`.
4. <a id="source-4"></a> [Linux cgroup v2 memory interface](https://docs.kernel.org/admin-guide/cgroup-v2.html#memory-interface-files) — `memory.current`, `memory.stat`, `memory.events`, `memory_localevents`; docs.kernel.org 7.3.0, read 2026-10-26.
5. <a id="source-5"></a> [memory.go](../../internal/tui/spaceview/memory.go#L11-L207) — `boxMemory`, `working`, `Model.memory`, `memScale`, `memRow`, details.
6. <a id="source-6"></a> [model.go](../../internal/tui/spaceview/model.go#L201-L215) — memory sort; `sampleCurrent` at L280-L284.
7. <a id="source-7"></a> [layout.go](../../internal/tui/spaceview/layout.go#L883-L923) — VM row; card stats L1324-L1331; compact VM row L1409-L1420 and box rows L1433-L1447.
8. <a id="source-8"></a> [popup.go](../../internal/tui/spaceview/popup.go#L889-L938) — `vmLines`.
9. <a id="source-9"></a> [data.go](../../internal/tui/spaceview/data.go#L157-L163) — aggregate OOM toast.
10. <a id="source-10"></a> [telemetry_test.go](../../internal/spacetelemetry/telemetry_test.go#L54-L73) — `TestParseMemStat`; `TestCollectorNamesUnreadMemoryCounters` L326-L357.
11. <a id="source-11"></a> [memory_view_test.go](../../internal/tui/spaceview/memory_view_test.go#L33-L160) — fill, stale and heat regressions.
12. <a id="source-12"></a> [memory_test.go](../../internal/tui/spaceview/memory_test.go#L40-L274) — estimate, cards, details, sort, pool and widths.
13. <a id="source-13"></a> [space_telemetry.go](../../cmd/yoloctl/space_telemetry.go#L180-L193) — inventory records the running container's ID.
14. <a id="source-14"></a> [memory_identity_test.go](../../internal/tui/spaceview/memory_identity_test.go#L1) — replacement and unknown identity; `TestStreamTelemetryCarriesBoxMemory` in cmd/yoloctl/space_telemetry_test.go.
