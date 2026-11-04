# Managed chat presence and box activity

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] Managed room membership is one shared, display-only snapshot under
`/run/yoloctl/controller/chat-presence/<room-id>/`, because each controller
request runs in its own worker. Every `space.telemetry` stream reads it while
building inventory; no mutation reads it. Only the stream holding a
non-blocking `flock` lease refreshes it from its own goroutine every 5 s. The
lease file is never removed. A stream's end, or a holder whose own inventory
is over 15 s old, releases it for takeover. The
directory, snapshot and lease belong to the controller's own identity, never
the native sandbox UID. Writes are atomic and no-follow; the lease is refused
unless it is a private regular file; reads are bounded (1 MiB),
owner/mode-checked and strictly decoded as exactly one value. [1](#source-1) [2](#source-2)
[3](#source-3)

[FACT] A refresh reads only the verified server control `status`
(peer-verified container, matching applied generation and configuration,
two-second bound). No participant box is inspected, dialled or asked about
its AIRC client or waiter. An account counts for a box only while its
declaration keeps the enrolled ID. Revoked, reused-name and unknown accounts
are external, and the human's connections are local. A stopped, undeclared or
unverifiable server is a room error, never an empty room; so is a pending
generation or configuration not yet applied. The snapshot records the
container the control peer proved; when the inventory saw another, nothing is
published. Readers reject a snapshot for another room or generation, another
server container, a missing file, a future stamp, or one older than 15 s on
the VM's `CLOCK_MONOTONIC`. It is the inventory's only room. The stream sends
an early inventory after its first publication. [4](#source-4) [5](#source-5)
[6](#source-6)

[FACT] Availability is each participant's own IRC state. The chat stream
marks a member away from a WHO `G` flag or an `AWAY` with a reason (bounded
to 120 runes) and clears it on an `AWAY` without one. One rule classifies
every chat participant: not connected; explicit away with its reason;
otherwise connected. Waiter or listener settings, agent sessions, room
errors, box activity, silence and telemetry freshness never change it.
[7](#source-7) [8](#source-8)

[FACT] `chat` opens the same telemetry read at space view's 1 s
`DefaultInterval` and reconnects with backoff. The panel uses the managed
room only to map a trusted, non-human box participant's authenticated
account to its box, while the stream is live, the inventory is under 15 s
old and the room reports no error. Activity then comes from the shared
`ActivityTracker` (terminal output or sustained CPU/disk load) and
`ActivityCurrent` gate, as in space view (see
[observed box activity](box-activity.md)). Every dot is a solid ●: green
connected, yellow away and gray not connected. A card's detail line puts
a box's observed model and effort (else nothing) or a human's `human`
before an away reason, so
truncation cuts the reason; a compact row shows the reason after the name and
any ` you` tag, with no kind or trust tag.
Neither adds an `away` label; a reasonless AWAY adds no text and keeps its
yellow dot. [9](#source-9) [10](#source-10) [11](#source-11) [14](#source-14)

[FACT] Both views animate activity with one helper, `Theme.ActiveTone`. The
dot keeps its hue and breathes from its own color to a 55% mix toward white
and back once per 2 s `PulseCycle`: a raised cosine of wall time with no off
phase. With motion off it holds the peak. Space view's RUNNING dot and LIVE
mark use its tick time; its steady tick is the shared 160 ms `PulseFrame`.
The chat panel uses its pulse tick's time and ticks every `PulseFrame` only
while a listed participant is active. Activity breathes whichever color is
shown; it never changes the glyph, hue or state. [12](#source-12)
[13](#source-13) [14](#source-14)

[FACT] Tests cover:
- connection mapping, explicit server errors and refresher sharing;
- takeover and kept leases;
- stale, future, mismatched and corrupt snapshots;
- stream and stalled-holder release;
- availability unchanged by absent or lost sessions, room errors, and stale
  or lost telemetry;
- AWAY and back, with and without a reason;
- card and compact away details at widths 80 and 100;
- shared-tracker activity, including away, idle, input, network or brief
  disk, stale and lost evidence;
- per-frame equality of both views' dots with `ActiveTone`;
- one solid glyph whose color only availability sets, active or not;
- the waveform's period, start, peak, hue, range and smoothness in both
  themes.

Server status is stubbed. No live VM, AIRC binary or terminal rendering is
exercised. [15](#source-15) [16](#source-16) [17](#source-17)
[18](#source-18)

## Sources

1. <a id="source-1"></a> [space_telemetry_chat.go](../../cmd/yoloctl/space_telemetry_chat.go#L141-L191) — `chatPresence`, root-owned `nativeChatPresence`, `presenceRel`; path in layout/chat.go `ChatPresenceDir`.
2. <a id="source-2"></a> [space_telemetry_chat.go](../../cmd/yoloctl/space_telemetry_chat.go#L295-L351) — `run`, `release`, owner-checked `acquire`.
3. <a id="source-3"></a> [space_telemetry_chat.go](../../cmd/yoloctl/space_telemetry_chat.go#L251-L275) — bounded single-value `read`; `publish` at L392.
4. <a id="source-4"></a> [space_telemetry_chat.go](../../cmd/yoloctl/space_telemetry_chat.go#L58-L139) — `managedRoomTelemetry`, `nativeServerStatus`; `refresh` (stale-holder release) at L353-L390.
5. <a id="source-5"></a> [space_telemetry_chat.go](../../cmd/yoloctl/space_telemetry_chat.go#L208-L248) — reader validation in `room`.
6. <a id="source-6"></a> [space_telemetry.go](../../cmd/yoloctl/space_telemetry.go#L195-L200) — inventory wiring of the managed room; stream lease lifetime and early inventory at L255-L285.
7. <a id="source-7"></a> [client.go](../../internal/chatstream/client.go#L534-L582) — WHO flags and `AWAY` in the roster.
8. <a id="source-8"></a> [presence.go](../../internal/spacetelemetry/presence.go#L1-L41) — `PresenceEvidence`, `ClassifyPresence`.
9. <a id="source-9"></a> [chat_view_telemetry.go](../../cmd/yoloctl/chat_view_telemetry.go#L12-L38) — `streamChatTelemetry`.
10. <a id="source-10"></a> [telemetry.go](../../internal/chatui/telemetry.go#L1-L142) — `participantBox`, `memberPresence`, pulse tick and frame time.
11. <a id="source-11"></a> [activity.go](../../internal/spacetelemetry/activity.go#L1-L239) — `ActivityCurrent`, `ActivityTracker`; classification rules in the box-activity digest.
12. <a id="source-12"></a> [theme.go](../../internal/tui/spaceview/theme.go#L95-L156) — `PulseCycle`, `PulseFrame`, `ActiveTone`, `lift`, `Color.rgb`; `Warn` at L20; `slowTick` in model.go L90.
13. <a id="source-13"></a> [layout.go](../../internal/tui/spaceview/layout.go#L1105-L1113) — `statePill` active dot; LIVE mark at L213-L217.
14. <a id="source-14"></a> [roster.go](../../internal/chatui/roster.go#L178-L274) — `memberCard` (its detail text from `roleLine` in marquee.go L66), `memberRuns`, `presenceDot`, `presenceLabel`.
15. <a id="source-15"></a> [space_telemetry_chat_test.go](../../cmd/yoloctl/space_telemetry_chat_test.go#L1-L362) — mapping, sharing, validation, ownership and stream.
16. <a id="source-16"></a> [presence_test.go](../../internal/spacetelemetry/presence_test.go#L1-L48) — rule and freshness gate.
17. <a id="source-17"></a> [presence_panel_test.go](../../internal/chatui/presence_panel_test.go#L1-L347) — explicit availability, AWAY/back, participant details, activity and shared waveform.
18. <a id="source-18"></a> [activity_test.go](../../internal/tui/spaceview/activity_test.go#L154-L327) — space view dot and `TestActiveToneBreathesGently`.
