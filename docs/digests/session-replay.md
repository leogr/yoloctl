# Authoritative session state and compatibility replay

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Before launching the sandbox SSH child, the holder creates a persistent
Ghostty terminal with `TERM=xterm-256color` and the native 64 MiB history budget.
Each managed viewer caps visible history with its own `scrollback_lines` setting
(default 10,000), without changing shared retention. The holder processes output
even without attached viewers.
Only this parser answers queries. Replies from each output batch are coalesced
into the bounded input queue; attached replicas never answer them.
[1](#source-1) [2](#source-2) [3](#source-3) [14](#source-14)

[FACT] Managed admission captures native state and registers the viewer under
one lock. The initial snapshot includes both screens, history, modes, colors,
title and parser continuation. Later output, geometry, theme metadata and
clipboard events have monotonically increasing sequence numbers. The holder
ends synchronized-output holds after one second and publishes a typed release;
replicas do not run independent hold timers. The client
rejects missing/reordered events. Restoration is quiet: historical queries,
bells and clipboard writes are not replayed as live effects. Snapshot limits
can temporarily refuse attachment during an oversized unfinished control.
[2](#source-2) [4](#source-4) [5](#source-5)

[FACT] All attachments remain interactive. Resizes are serialized through the
canonical terminal and SSH resize request; the latest accepted size applies to
the shared session. Viewers clip that common grid to their local display.
The earliest connected managed viewer with measurements supplies host color
defaults; disconnect selects the next measured viewer. Unknown entries retain
previous measurements, including while detached. Without any measurement,
unknown palette/default queries remain pending rather than inventing host
colors. App overrides remain native terminal state. [2](#source-2)
[3](#source-3) [10](#source-10)

[FACT] Every viewer has an independent writer and immutable queued events.
Socket writes do not hold the parser lock. The live queue allows 4 MiB/4096
packets including in-flight bytes; the private snapshot is separate. Overflow
or a five-second write timeout closes that viewer without inventing agent exit.
Shutdown stops admission and drains accepted output before EXIT, under a shared
five-second deadline. An input buffer overflow is an explicit failure. A
canonical parser/reply failure closes the sandbox connection, leaving an
uncertain-session marker instead of continuing with divergent state.
[1](#source-1) [2](#source-2) [6](#source-6)

[FACT] `--passthrough` uses a separate native stream to retain recognized UTF-8
text, display controls and input modes. Queries and opaque/unknown extensions,
including DCS/APC and graphics, are omitted. Mode 2048 size notifications are
removed, preserving other modes in the same control. Incomplete controls wait for a
native parser boundary; invalid UTF-8 and controls exceeding 2 MiB are dropped.
This is a compatibility view, not a byte-transparent terminal tunnel.
[7](#source-7) [8](#source-8)

[FACT] Its bounded ring excludes OSC 52 and neutralizes executed bells with
NUL. A separate 1×1/no-history native model tracks evicted prefix input state.
Replay restores both Kitty stacks, selected current/saved DEC modes, effective
mouse modes, screen and modifyOtherKeys before the retained tail. Native
continuation is capped at 32 KiB; recovery advances through the next retained
parser boundary or refuses attachment. Arbitrary old screen contents/history
cannot be recovered from this transcript. [1](#source-1) [9](#source-9)

[FACT] Tests cover complete screen restoration beyond the byte ring, mixed
interactive views with one query response, quiet historical effects, chunked
clipboard events beside partial controls, query bursts, slow-client isolation,
exit ordering, replay/live cuts, shared resizes, theme handoff, per-view history
and synchronized-output release beside unfinished controls. Compatibility tests check every split of
selected queries and preserve Unicode titles/input controls. Core replay tests
exercise evicted keyboard stacks and future operations. These are local
contracts, not live Lima or graphical-terminal parity evidence.
[8](#source-8) [11](#source-11) [12](#source-12) [13](#source-13)

[FACT] The isolated attach smoke drives TUI, managed and passthrough views
together. Its guest audit checks input order, resizing during output, one reply
per cursor query and completion. The driver checks unread-viewer isolation,
detach/rejoin and exit. Managed rejoin waits for guest-confirmed input/query
delivery and subsequent receive progress; it does not require a contiguous
substring in renderer paint bytes. It waits for flood acknowledgment before independent
resizes; the probe preserves resize signals received during sampling. Setup
stages source, argv and environment through confined `box exec` pipes; only a
short file-launch command enters the PTY. Local tests exercise fragmented paints,
missing delivery/stalled-output rejection, pipe setup, delayed input, overlapping
resize samples, canonical/Readline startup, deadlines and corrupt-audit rejection.
These tests do not establish a live-VM pass or graphical parity.
[15](#source-15) [16](#source-16) [17](#source-17)

## Sources

1. <a id="source-1"></a> [holder.go](../../internal/session/holder.go#L71) — `RunHolder`, `broadcast`, `retainReplay`, shutdown and marker outcomes.
2. <a id="source-2"></a> [holder_view.go](../../internal/session/holder_view.go#L18) — `initView`, `flushRepliesLocked`, `resizeView`, `selectColorsLocked`, `admitView`.
3. <a id="source-3"></a> [colors.go](../../internal/terminal/colors.go#L60) — shared `ColorQueries`, native overrides, bounded pending reports.
4. <a id="source-4"></a> [view.go](../../internal/session/view.go#L145) — `attachView`, identity, snapshot and ordered event validation.
5. <a id="source-5"></a> [Native snapshots](terminal-snapshots.md) — format, bounds, restoration and omitted resources.
6. <a id="source-6"></a> [holder_client.go](../../internal/session/holder_client.go#L9) — per-viewer queue, deadlines and writer.
7. <a id="source-7"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L224) — native `PassthroughHandler` and bounded control publication.
8. <a id="source-8"></a> [passthrough_test.go](../../internal/terminal/passthrough_test.go#L11) — receiving-parser query suppression and byte-split UTF-8/control preservation.
9. <a id="source-9"></a> [terminal_state.go](../../internal/session/terminal_state.go#L9) — evicted-prefix state, bell filter, replay and recovery.
10. <a id="source-10"></a> [managed.go](../../internal/tui/term/managed.go#L98) — replica publication, canonical geometry and measured metadata.
11. <a id="source-11"></a> [holder_view_test.go](../../internal/session/holder_view_test.go#L96) — snapshot, mixed-view, clipboard and query-burst regressions.
12. <a id="source-12"></a> [holder_fanout_test.go](../../internal/session/holder_fanout_test.go#L140) — backpressure, order, queue ownership, exit and shutdown tests.
13. <a id="source-13"></a> [terminal_state_test.go](../../internal/session/terminal_state_test.go#L68) — overflow, continuation and input-state comparisons.
14. <a id="source-14"></a> [managed.go](../../internal/tui/term/managed.go#L98) — per-view history cap and typed hold release; native/local separation in `internal/terminal/frame.go:SetHistoryViewLimit`; regressions in `managed_test.go`.
15. <a id="source-15"></a> [attach-mixed.py](../../scripts/smoke/attach-mixed.py#L143) — `prepare_python_probe`, `Driver.start_flood`, `check_live_rejoin`, `wait_audit` and `run`, invoked by `box-attach.sh`.
16. <a id="source-16"></a> [test_attach_mixed.py](../../scripts/test_attach_mixed.py#L22) — local PTY driver/probe, fragmented repaint/rejoin, canonical/Readline startup and audit negative controls.
17. <a id="source-17"></a> [attach-probe.py](../../scripts/smoke/attach-probe.py#L20) — guest `run`: raw input, paced output, resize and cursor-reply audit.
