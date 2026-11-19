# Log steps and the wire form

Verification: `dc857939b14bace057ca9c009367a0672facbaa3` and accompanying changes. Step ordering re-read at `0a1e47d47001eab39274faf83f3af36419b04f2b` and accompanying changes.

[FACT] A step is records only: `running` once, then at most one end state, with
`took`, `err` or `reason`; executor output lines are lossy detail records. A
`Tracker` keeps at most 200 steps, 64 KiB of detail per step and 1 MiB in
total, each line counted with its newline so empty lines are bounded too, evicting the oldest ended step's lines first (else the oldest running
one) and counting omitted bytes. States only move forward, and `Snapshot`
records restore every field in an empty tracker. [1](#source-1)
[2](#source-2)

[FACT] The wire form is a length-prefixed JSON object of at most 8 KiB with
typed values; values of any other type, and non-finite floats, travel as opaque text that never
decodes to a string, so a wrongly typed reserved value stays a field. Decoding
rejects unknown keys and anything but whitespace after the object. Optional attributes, then `err`, `reason` and the message are
cut to fit; a record whose structure cannot fit is never sent partially: the
writer remembers `ErrUnrepresentable` and sends one ordinary warning instead,
or nothing when even that cannot fit. A worker-side Logger always passes step,
detail and snapshot records so the host can show a failed step's output.
[3](#source-3) [2](#source-2)

[FACT] Relayed worker records go to the process Logger when the command's
stderr is the process stderr, else as plain lines to that writer; a rendering
error is reported once without changing the result. On a terminal at the
default `info` level, `stepview` draws a live block within the terminal's
bounds. Recent successful and skipped children can share the block with running
steps. Their final lines follow the parent's, in sibling completion order.
Failed and uncertain children print once when they end, with bounded tails.
Suspension and partial output defer writing. Snapshots retain failure
diagnostics without replaying completed successes. At close an unfinished root
is marked uncertain before its remaining grouped children. [4](#source-4) [5](#source-5)

[FACT] The host stderr logger and the client's plain fallback stream progress,
debug detail, failures and uncertain results. They hold successful and skipped
child lines for an observed parent, subject to log level. Close and the buffer
limits release lines of incomplete groups. Below debug, a failed relayed step's
last 20 lines follow it as bounded error records. Background files and `space
logs` retain record order. `space operation` reads receipts through controller
status/list requests. [6](#source-6) [4](#source-4) [7](#source-7) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Steps and Tracker](../../pkg/prettylog/tracker.go#L71-L311) — `Apply`, `restore`, `lineCost`, `addLine`, `victim`, `Snapshot`; step.go `StartStep`, `Detail`, `end`.
2. <a id="source-2"></a> [Step and wire regressions](../../pkg/prettylog/behavior_test.go#L84-L397) — review1_test.go bounded empty lines, last reserved key, strict decoding, unambiguous fold keys; step lines, snapshot round trip through the wire at Quiet, monotonic states, eviction, typed round trip, caps, unrepresentable records, incomplete streams, `Route`.
3. <a id="source-3"></a> [Wire](../../pkg/prettylog/wire.go#L19-L328) — `Opaque`, `MarshalRecord`, `UnmarshalRecord`, `atEOF`, `writeWire`, `WireReader.Next`.
4. <a id="source-4"></a> [Host record routing](../../internal/controller/client.go#L45) — `clientLogStreams`, `HostLogSink`, `case "log"`; cmd/yoloctl stepview.go `installStepView`, controller_client.go `callControllerTerminal`.
5. <a id="source-5"></a> [Steps block](../../pkg/prettylog/stepview/stepview.go#L1) — `Apply`, `blockRows`, `summaryLocked`, `Writer`, `Suspend`, `Close`; chronology_test.go and stepview_test.go screen checks.
6. <a id="source-6"></a> [Process logger](../../internal/logging/logging.go#L237-L280) — `Init`, `InitWorker`, `InitFile`.
7. <a id="source-7"></a> [Line summaries](../../pkg/prettylog/step_lines.go#L1) — `groupStepLine`, `flushStepLines`; `step_lines_test.go`; stepview/failures.go `Failures.expand`; internal/logging `Init`; cmd/yoloctl logflags.go `executeRoot`.
8. <a id="source-8"></a> [Stored logs](../../cmd/yoloctl/space_log_render.go#L44) — `newLogView`, `Write`, `emit`; host_log.go `tailHostLogWithSettings`; controller_lifecycle.go `newSpaceOperationCmd`.
