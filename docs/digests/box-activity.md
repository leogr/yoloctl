# Observed box activity

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Each session holder keeps monotonic workload counters for its own
process lifetime: bytes read from the agent's PTY, and accepted, non-empty
participant input packets and bytes. The PTY count is taken at the output
read, before title rewriting. Holder-synthesized titles, compatibility replay,
native snapshots, viewer fan-out and the holder's own terminal query replies
are not counted. [1](#source-1) [2](#source-2)

[FACT] A stats request is a HELLO v5 admission whose first client frame is
`frameStats` instead of a subscription. It requires the exact bundle and
terminal core, gets one bounded reply and closes. It registers no viewer,
sends no snapshot or viewer frame, changes no geometry, input or counter, and
is not logged. The reply carries a digest of the holder
incarnation, not the incarnation. Holders from other builds close or refuse;
the reader reports an error, never zero counters. One read is bounded to
500 ms. [2](#source-2) [3](#source-3) [4](#source-4)

[FACT] The `space.telemetry` sampler asks only for running boxes whose
boxstate session is `running`; a running box whose session state is
`unknown` gets a `session state unknown` `pty_error` instead. It uses the root-only local attach socket
beside the holder. A sample's reads run concurrently (at most eight) under one
shared 500 ms deadline derived from the stream context. Stalled holders
therefore cost the sample their evidence, not one bound each, and the reads
end with the stream. A failed read becomes a per-box `pty_error`, independent of
cgroup metrics. Each box sample names its container ID. It also names unread
or unparseable `io.stat` and `net/dev` counters, which are never reported as
zero bytes. [5](#source-5) [6](#source-6) [7](#source-7)

[FACT] The host classifies each running box as active, idle or unknown:
- Any increase in the managed session's PTY output bytes is activity, with no
  byte floor, compared only within one holder session and container. A new
  holder or container, or a read after a failed one, is a new baseline that
  drops the hold.
- So is sustained load: a sample pair is busy at 0.25 core of box cgroup CPU
  or 1 MiB/s of `io.stat` reads plus writes over its VM-uptime interval, and
  busy pairs count once they cover 3 s of uptime unbroken. A pair over 2 s, a
  missing sample, a new container, a decreasing CPU counter or a cgroup error
  starts over and drops the hold. An unread or decreasing disk counter only
  removes disk from that pair; CPU still counts and an existing hold runs
  out normally. These defaults are provisional.
  Accepted input and network never count.
- Either keeps a box active for two sample pairs; the output hold lasts only
  while output stays readable, and an ended session or unsampled box drops it.
- Load only adds activity: an unread output counter, including an unknown
  session state, is unknown unless loaded, and a box without a session is
  idle unless loaded.

The shared `spacetelemetry.ActivityTracker` applies these rules for space view
and the chat participants panel, sampling at the shared
`DefaultInterval` of 1 s. [8](#source-8) [16](#source-16)

[FACT] Evidence is current only while the stream is live, unpaused and its
last sample arrived within max(3 intervals, 2 s) of host time. A lost stream,
a resumed pause, or a sample after such a stall makes the next sample a fresh
baseline; the last CPU values stay on screen, while memory shows no current
reading. [9](#source-9) [15](#source-15) [17](#source-17)

[FACT] In space view, only the RUNNING dot shows activity; the pill label stays the box
state. Active dots breathe through the shared `Theme.ActiveTone`, a smooth
2 s cycle that the chat participants panel also uses (with motion off, a
static bright tone). Idle dots are steady, and unknown dots are dimmed. The
hovered box's footer names the state and why it is unknown, and the help
legend explains the dots. [10](#source-10) [11](#source-11)

[FACT] Tests cover:
- counting only workload bytes;
- input, network and brief or light load never activating, and sustained CPU
  or disk activating after 3 s of uptime under fast or gapped sampling,
  resets, unread counters and new containers;
- the hold dropping on unread output, an ended session or a stopped box;
- unchanged counters through query replies, replay, snapshots and stats reads;
- viewer exclusion, exact builds, older holders and the read bound;
- session-only sampling, unknown session state and unread counters;
- holder or container replacement, stale or lost streams, and the dot.

No live VM calibration is recorded here.
[12](#source-12) [13](#source-13) [14](#source-14) [16](#source-16)

## Sources

1. <a id="source-1"></a> [holder.go](../../internal/session/holder.go#L239-L244) — PTY read count; input count at L550; counters at L398-L403.
2. <a id="source-2"></a> [stats.go](../../internal/session/stats.go#L18-L119) — `Stats`, `serveStats`, `sessionDigest`, `ReadStats`, `readStats`.
3. <a id="source-3"></a> [holder_view.go](../../internal/session/holder_view.go#L234-L237) — stats admission branch.
4. <a id="source-4"></a> [protocol.go](../../internal/session/protocol.go#L65-L67) — `frameStats`.
5. <a id="source-5"></a> [space_telemetry.go](../../cmd/yoloctl/space_telemetry.go#L122-L141) — `telemetryPTYStats`, `liveBox.session`/`sessionUnknown`; unknown-session regression in space_telemetry_test.go `TestTelemetryUnknownSessionIsUnreadOutput`.
6. <a id="source-6"></a> [collect.go](../../internal/spacetelemetry/collect.go#L19-L25) — shared deadline and reader bound; `Target` at L30-L39, container ID at L98, unread counters at L215-L230, `ptys` at L234-L287.
7. <a id="source-7"></a> [telemetry.go](../../internal/spacetelemetry/telemetry.go#L185-L226) — `Container`, `Unavailable`, `PTY`, `PTYCounters`.
8. <a id="source-8"></a> [activity.go](../../internal/spacetelemetry/activity.go#L19-L239) — thresholds, `DefaultInterval`, `boxActivity.observe`, `busyPair`, `ActivityTracker`; space view's use in data.go `applySample`; chat's `TelemetryInterval` in chatui/telemetry.go.
9. <a id="source-9"></a> [model.go](../../internal/tui/spaceview/model.go#L278-L294) — `staleAfter`, `sampleCurrent`, `Model.activity`; stall check at L382-L384.
10. <a id="source-10"></a> [layout.go](../../internal/tui/spaceview/layout.go#L1078-L1113) — `statePill`; hint at L343-L352; `ActiveTone` in theme.go L79-L140.
11. <a id="source-11"></a> [popup.go](../../internal/tui/spaceview/popup.go#L381-L384) — help legend.
12. <a id="source-12"></a> [stats_test.go](../../internal/session/stats_test.go#L42) — holder counter, admission and bound tests.
13. <a id="source-13"></a> [telemetry_test.go](../../internal/spacetelemetry/telemetry_test.go#L252-L389) — session-only and unknown-session sampling, unread counters and stalled holders.
14. <a id="source-14"></a> [activity_test.go](../../internal/tui/spaceview/activity_test.go#L39) — classification, freshness and dot.
15. <a id="source-15"></a> [data.go](../../internal/tui/spaceview/data.go#L149-L155) — `rebaseline`; `ActivityTracker.Rebaseline` in activity.go.
16. <a id="source-16"></a> [activity_test.go](../../internal/spacetelemetry/activity_test.go#L53-L190) — sustained load, spikes, continuity and resets.
17. <a id="source-17"></a> [Space view memory readings](space-view-memory.md) — memory freshness.
