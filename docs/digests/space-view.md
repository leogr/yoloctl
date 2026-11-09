# Live space view

Verification: `2e70c9288f335135add2780e2607fa818ce8341c` and accompanying changes; `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `space view [SPACE]` is a host-only command added beside export and
import; its positional completes running spaces. It refuses box operators and
non-terminal output. It reads with `--interval` (250ms to 10s, default 1s).
[1](#source-1) [2](#source-2) [3](#source-3) [26](#source-26)

[FACT] Data arrives on one long-lived internal read, `space.telemetry`: a
registered non-mutating, host-only operation handled before Cobra. Each
connection first runs the ordinary bundle/running-VM check, so the view never
boots a VM. Reads are not counted as active work, so the stream neither takes
the native lock nor delays drain. When the stream fails, the view reconnects
with 1-15s backoff and shows the error; a stopped space stays visibly
unavailable. [4](#source-4) [5](#source-5) [6](#source-6) [7](#source-7)
[8](#source-8)

[FACT] The worker writes one JSON frame per line, each at most 4 MiB, and the
host decoder rejects any other version or shape. An inventory frame is sent
immediately and then every 5s: grants, defaults, the box resolver at
`RuntimeSummary` depth, each running box's observed container ID, effective
features and capabilities, the declared `privileged` setting, and observed chat rooms. Account state leaves are refreshed every 30s. Unavailable boxes or
accounts carry an error and are never shown as empty. Sample frames carry raw
cumulative counters, and the host derives rates from consecutive samples.
[9](#source-9) [10](#source-10) [11](#source-11)

[FACT] One `nerdctl inspect` returns each running box's ID, init PID, bridge IP,
start time and launch details, which `ClassifyRuntime` turns into
`runtime_mode`, its reason and `runtime_privileged` (any privileged signal). A
failed lookup or undecodable entry leaves the mode empty, never confined.
[12](#source-12) [33](#source-33) Counters are bounded no-follow reads of `/proc` and cgroupfs.
The sampled cgroup must be the workload pool's direct child whose name contains
the container ID; an unrelated cgroup becomes a per-box error.
[12](#source-12) [13](#source-13)

[FACT] Memory readings, their scales and labels, and when they count as current
are in [space view memory](space-view-memory.md). [32](#source-32)

[FACT] Chat links are observed, not declared. The only room is the managed
room, from its verified server's per-account connection counts; box features
and network listeners never create one. See [chat presence](chat-presence.md).
[10](#source-10)

[FACT] The layout is a pure function from data and UI state to a cell canvas
with click targets. It uses four to one card columns from 160/120/80 columns,
and a compact list below 48. It never exceeds the terminal dimensions; below
20×7 it shows a resize hint. The dashboard scrolls only vertically. Cards show `yolo.*` items as raised pills, with the operator
capability in a warning color. A feature that grants its same-named capability
shows once, as the capability. Members of a chat room share a colored corner
marker; hovering the room highlights its members. A declared or evidenced
privileged box gets a first warning pill, `▲ privileged`, suffixed
`· running confined` or `· runtime ?` when a running container's mode differs
or is unknown, and `▲ privileged runtime · declared confined` for an undeclared
box with any privileged signal; other boxes stay unmarked. Narrow cards and the
compact list drop the suffix. Hovering it explains it; a click opens
`box inspect`. [14](#source-14) [15](#source-15) [16](#source-16) [34](#source-34)

[FACT] When they fit, the space's counts and VM uptime join the title row,
in words and then as bare counts; otherwise they open the body. The title
row, or the resize hint, is the first row: explicit RGB black across the full
width in every theme, in the dark palette's colors. There the operator
warning and the privileged-box count are bold warning-colored text, not red
pills, in words from 90 columns when the row has room and otherwise as bare
counts, never cut off. [31](#source-31) The VM frame
is labelled `<space> · <backend> VM`, right-aligned, shortening the name
before pipe arrows; only the space name is bold. The box legend precedes
the CPU graph and aligns its markers with the graph fill. Chat counters,
room markers and help share the `◈` icon with native chat. Agent icons take a
two-column slot, as in the tab strip. RUNNING dots show
[observed activity](box-activity.md). [14](#source-14) [27](#source-27) [28](#source-28) [29](#source-29) [30](#source-30)

[FACT] Popup overlays erase whole wide graphemes at their edges, preserving the
canvas's cell count when a host path contains wide text. [16](#source-16)
[24](#source-24)

[FACT] Clicking a box attaches. A stopped box, a container without a live
session, or an undeclared container first asks for confirmation, because
`box attach` would start it. A lost or invalid box opens inspect instead. In a
plain terminal, the view hands the terminal to `box attach` and resumes after
detach. [17](#source-17)

[FACT] Inside the TUI, `space view` and native `chat` alone may request new
attach tabs through the private channel; space view sends plain requests.
Eligible lifecycle panes use that framing only for retained resume-target
metadata, which cannot open a tab.
The view sends `{op, space, box}` lines on it. The TUI accepts only `attach`
with valid names, then opens or refocuses a `box attach NAME --space SPACE` tab.
Children of the view never inherit that channel or the session channel.
[18](#source-18) [19](#source-19) [20](#source-20) [2](#source-2)

[FACT] Other items open popups that run the same binary with the matching
command's `--json` output: `account show`, `box inspect`, `agent show`,
`feature show`, `capability show` and `port list`. The VM, data disk, workdirs, grants and rooms
render live from the stream. The system and data disk band, its warnings and
`space storage` are in [VM disks](space-view-disks.md). While a popup is open, only it reacts: the
dashboard behind ignores hover and keys, and a click outside only closes it.
A published TCP port can be opened on the host unless `seams.browser` is off.
[21](#source-21) [17](#source-17) [2](#source-2)

[FACT] Tests cover parsers and confinement, frame bounds, stream ordering and
room derivation. They also cover layout at every width, clicks, confirmation,
popups, privilege pills and the tab channel through a PTY child. The inspect
fixture follows the source launch contract, not a live capture. These automated tests use fixtures
and local PTYs; they do not establish live-VM or proxy-confined chat coverage.
[22](#source-22) [23](#source-23) [24](#source-24) [25](#source-25)

## Sources

1. <a id="source-1"></a> [space_view.go](../../cmd/yoloctl/space_view.go#L30-L139) — `newSpaceViewCmd`, `runSpaceView`.
2. <a id="source-2"></a> [space_view.go](../../cmd/yoloctl/space_view.go#L143-L244) — `streamSpaceView`, `runViewCommand`, `viewChildEnv`.
3. <a id="source-3"></a> [controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L43) — host-only registration.
4. <a id="source-4"></a> [controller_internal.go](../../cmd/yoloctl/controller_internal.go#L82-L83) — `space.inventory` and `space.telemetry` registry entries.
5. <a id="source-5"></a> [controller_views.go](../../cmd/yoloctl/controller_views.go#L244-L256) — typed internal views.
6. <a id="source-6"></a> [controller_deploy.go](../../cmd/yoloctl/controller_deploy.go#L83-L111) — `checkControllerSpace`.
7. <a id="source-7"></a> [server.go](../../internal/controller/server.go#L108-L115) — reads are admitted without active-work accounting.
8. <a id="source-8"></a> [run.go](../../internal/tui/spaceview/run.go#L20-L78) — `Run`, `keepConnected`.
9. <a id="source-9"></a> [telemetry.go](../../internal/spacetelemetry/telemetry.go#L25-L253) — frame types, limits and decoder.
10. <a id="source-10"></a> [space_telemetry.go](../../cmd/yoloctl/space_telemetry.go#L27) — cadences, `telemetryAccounts`, inventory (managed room at L200, launch evidence at L192-L195) and `streamTelemetry`.
11. <a id="source-11"></a> [data.go](../../internal/tui/spaceview/data.go#L1) — host rates, history and transitions.
12. <a id="source-12"></a> [runtime_state.go](../../internal/container/runtime_state.go#L31) — `RuntimeStates`; shared `inspectEntry` decoding in `container.go` at L592.
13. <a id="source-13"></a> [collect.go](../../internal/spacetelemetry/collect.go#L14-L193) — bounded reads, `Sample`, `BoxCgroup`.
14. <a id="source-14"></a> [layout.go](../../internal/tui/spaceview/layout.go#L101-L152) — breakpoints and `render`.
15. <a id="source-15"></a> [layout.go](../../internal/tui/spaceview/layout.go#L1249) — `yoloRow`; `chatRows` at L972.
16. <a id="source-16"></a> [canvas.go](../../internal/tui/spaceview/canvas.go#L1) — cell canvas and fitting.
17. <a id="source-17"></a> [model.go](../../internal/tui/spaceview/model.go#L528-L638) — `mouse`, `activateBox`, `attach`.
18. <a id="source-18"></a> [tab_requests.go](../../internal/tui/tab_requests.go#L17-L43) — request handler and validation.
19. <a id="source-19"></a> [session.go](../../internal/tui/term/session.go#L106) — request channel for a tab child.
20. <a id="source-20"></a> [tabrequest.go](../../internal/termio/tabrequest.go#L16-L69) — request wire format.
21. <a id="source-21"></a> [popup.go](../../internal/tui/spaceview/popup.go#L53) — popup loading and JSON decoders.
22. <a id="source-22"></a> [telemetry_test.go](../../internal/spacetelemetry/telemetry_test.go#L1) — parser, room and collector tests.
23. <a id="source-23"></a> [space_telemetry_test.go](../../cmd/yoloctl/space_telemetry_test.go#L1) — stream tests.
24. <a id="source-24"></a> [view_test.go](../../internal/tui/spaceview/view_test.go#L1) — layout and interaction tests.
25. <a id="source-25"></a> [tabrequest_test.go](../../internal/tui/term/tabrequest_test.go#L1) — PTY child request test.
26. <a id="source-26"></a> [completion.go](../../cmd/yoloctl/completion.go#L82) — running-space completion.
27. <a id="source-27"></a> [layout.go](../../internal/tui/spaceview/layout.go#L160) — `titleBar` (warnings at L204-L224, fitted at L254); `summaryChips` L593, `frameTop` L800, `vmLabel` L833, legend L895.
28. <a id="source-28"></a> [model.go](../../internal/tui/spaceview/model.go#L226-L239) — `agentIcon` two-column slot.

29. <a id="source-29"></a> [Shared visual helpers](../../internal/tui/spaceview/visual.go#L9) — `ChatIcon`.
30. <a id="source-30"></a> [Layout regressions](../../internal/tui/spaceview/view_test.go#L518) — `TestVMFrameNamesTheSpaceRightAligned`, `TestLegendPrecedesTheCPUGraph`.
31. <a id="source-31"></a> [First row](../../internal/tui/spaceview/theme.go#L79-L93) — `FirstRowBG`, `FirstRow`; `putFirstRow` in canvas.go L218; first_row_test.go `TestFirstRowIsBlackInEveryState`, `TestFirstRowOperatorWarningStaysDistinct`.
32. <a id="source-32"></a> [Space view memory readings](space-view-memory.md) — VM, pool and box memory presentation.
33. <a id="source-33"></a> [privileged.go](../../internal/agentbox/privileged.go#L78-L103) — `ClassifyRuntime`; `TestParseRuntimeStatesCarriesLaunchEvidence` in `runtime_state_test.go` at L43, `TestTelemetryInventoryCarriesPrivilege` in `space_telemetry_test.go` at L319.
34. <a id="source-34"></a> [layout.go](../../internal/tui/spaceview/layout.go#L1309-L1338) — `privilegeMark`, `privilegedPill`; hint at L410, compact chip at L1516, click in `popup.go` at L166; `privileged_test.go` and its `privilegedFixtureModel` in `fixture_test.go`.
