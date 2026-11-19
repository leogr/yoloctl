# Log presentation

Verification: `dc857939b14bace057ca9c009367a0672facbaa3` and accompanying changes. Step ordering re-read at `0a1e47d47001eab39274faf83f3af36419b04f2b` and accompanying changes. Place labels at `c6e67053e4fd40a8987dc6be1c3f34bb4d6b1380` and accompanying changes.

[FACT] `pkg/prettylog` is a public package of the yoloctl module that renders
`log/slog` records. It imports no yoloctl or UI package and reads no terminal.
A record's presentation kind comes from one shared parse of its reserved
top-level attributes: only the last occurrence of a reserved key counts, and
if its value has the wrong type the key is absent and that value is an ordinary
field (earlier occurrences are ignored everywhere, including on the wire). An
`outcome` or step `state` decides first, then the level. Each
kind has one glyph (`✗ ! • › ✓ ? · ○`) and one plain prefix (`error:`,
`warning:`, `notice:`, `uncertain:`, `debug:`). [1](#source-1) [2](#source-2)

[FACT] Pretty lines are `glyph label  message  fields  took`, plain lines
`[time ]prefix label: message fields (took)`, keeping the message's case
([labels](log-labels.md)). [3](#source-3)
Controls in labels, messages, keys, group names and values
render as `\xNN`; a message newline becomes an aligned continuation line. Terminal width and hanging-wrap rules are in [terminal wrapping](log-wrapping.md). An
`err` attribute is shown with its record at every verbosity, as a continuation
line or `: err`. Display levels: `error` shows errors and uncertain results
(failed and unknown outcomes included); `warn` adds warnings and every final
outcome; `info` (default) adds notices and progress; `debug` adds debug records
and step detail lines.
[3](#source-3) [4](#source-4) [11](#source-11)

[FACT] A `Logger` serializes emission through one mutex shared by every
derived handler. Writes are synchronous, write errors are remembered and
never change results, and the writer is never closed.
Consecutive identical records (same level, message and typed attributes, under
a length-prefixed key) within the fold window print once, followed by
`↳ repeated N more times until HH:MM:SS` when the run ends (different record,
timer, `Flush` or `Close`); outcomes, steps and details never fold. `Route`
sends step records to a sink and other lines to another writer. [4](#source-4)
[11](#source-11)

[FACT] An optional `Options.Emit` sink receives tagged structured record or
repeat-summary decisions from the same filter/fold engine instead of byte
writes. `HandleOwned` associates an opaque local owner, retained only for the
active run's first record; it never enters attributes, fold keys or wire data.
Summary metadata carries that owner/count/time; the structured record requires
consumer domain sanitization. Calls are synchronous under the logger mutex,
including timer/close, so a sink must neither reenter it nor block on a UI event
loop. Nil preserves existing bytes; wire framing and step routing are unchanged.
[17](#source-17) [18](#source-18)

[FACT] A step is records only: `running` once, then at most one end state, with
`took`, `err` or `reason`; executor output lines are lossy detail records. A
`Tracker` keeps at most 200 steps, 64 KiB of detail per step and 1 MiB in
total, each line counted with its newline so empty lines are bounded too, evicting the oldest ended step's lines first (else the oldest running
one) and counting omitted bytes. States only move forward, and `Snapshot`
records restore every field in an empty tracker. [5](#source-5)
[12](#source-12)

[FACT] The wire form is a length-prefixed JSON object of at most 8 KiB with
typed values; values of any other type, and non-finite floats, travel as opaque text that never
decodes to a string, so a wrongly typed reserved value stays a field. Decoding
rejects unknown keys and anything but whitespace after the object. Optional attributes, then `err`, `reason` and the message are
cut to fit; a record whose structure cannot fit is never sent partially: the
writer remembers `ErrUnrepresentable` and sends one ordinary warning instead,
or nothing when even that cannot fit. A worker-side Logger always passes step,
detail and snapshot records so the host can show a failed step's output.
[6](#source-6) [12](#source-12)

[FACT] Colour is decided once for the final writer: a terminal gets the pretty
format, other writers plain; `auto` colours a terminal unless `NO_COLOR` is set
or `TERM=dumb`; `always` and `never` force it. Until a program that owns input
reports the terminal background, the palette is the terminal's own 16 colours
plus faint. `logtheme.SetBackground` maps the dashboard theme's roles (`Bad`,
`Warn`, `Label`, `Text`, `OK`, `Dim`, `Brand`) instead. [7](#source-7)
[8](#source-8)

[FACT] `internal/logging` holds the actor/executor vocabulary and the process
Logger: `Init` (host stderr), `InitWorker` (wire), `InitFile` (plain, UTC
RFC 3339) each install it as slog's default once; `SetLogger` swaps it for
tests. It has no UI dependency. The root command's persistent
`--log-level debug|info|warn|error`, `-q` (`warn`) and `--color` win over
`YOLOCTL_LOG` (same values) and `YOLOCTL_DEBUG=1` (`debug`); `-q` with another
level is an error, and `-v` stays Cobra's `--version` shorthand. `main` renders
an unsilenced command error once through the `renderCLIError` hook and closes
the Logger. The CLI migration uses structured error/uncertain outcomes and
stderr-only command logs; data writers remain separate.
[8](#source-8) [9](#source-9) [13](#source-13) [16](#source-16)

[FACT] Relayed worker records go to the process Logger when the command's
stderr is the process stderr, else as plain lines to that writer; a rendering
error is reported once without changing the result. On a terminal at the
default `info` level, `stepview` draws a live block within the terminal's
bounds. Recent successful and skipped children can share the block with running
steps. Their final lines follow the parent's, in sibling completion order.
Failed and uncertain children print once when they end, with bounded tails.
Suspension and partial output defer writing. Snapshots retain failure
diagnostics without replaying completed successes. At close an unfinished root
is marked uncertain before its remaining grouped children. [14](#source-14) [15](#source-15)

[FACT] The host stderr logger and the client's plain fallback stream progress,
debug detail, failures and uncertain results. They hold successful and skipped
child lines for an observed parent, subject to log level. Close and the buffer
limits release lines of incomplete groups. Below debug, a failed relayed step's
last 20 lines follow it as bounded error records. Background files and `space
logs` retain record order. `space operation` reads receipts through controller
status/list requests. [8](#source-8) [14](#source-14) [19](#source-19) [20](#source-20)

[FACT] The holder logs an agent launch as its program name and argument count,
never its argv. Chat notices and delivery marks use the shared `✗`/`?` glyphs
and the theme's `Warn` tone. [10](#source-10)

## Sources

1. <a id="source-1"></a> [Kinds and glyphs](../../pkg/prettylog/level.go#L104-L176) — `KindOf`, `kindOf`, `DefaultGlyphs`, `PlainPrefix`.
2. <a id="source-2"></a> [Reserved parse](../../pkg/prettylog/structure.go#L26-L181) — `StructureOf`, `eachAttr`, `validReserved`, `apply`, `IsLossy`.
3. <a id="source-3"></a> [Line grammar](../../pkg/prettylog/format.go#L50-L420) — `escape`, `cut`, `fieldsText`, `render`, `renderPlain`, `summary`.
4. <a id="source-4"></a> [Logger](../../pkg/prettylog/logger.go#L43-L458) — `Verbosity`, `allows`, `wireAllows`, `New`, `Route`, `Close`, `emit`, `arm`, `endRun`, `handler`.
5. <a id="source-5"></a> [Steps and Tracker](../../pkg/prettylog/tracker.go#L71-L311) — `Apply`, `restore`, `lineCost`, `addLine`, `victim`, `Snapshot`; step.go `StartStep`, `Detail`, `end`.
6. <a id="source-6"></a> [Wire](../../pkg/prettylog/wire.go#L19-L328) — `Opaque`, `MarshalRecord`, `UnmarshalRecord`, `atEOF`, `writeWire`, `WireReader.Next`.
7. <a id="source-7"></a> [Colour](../../pkg/prettylog/color.go#L38-L140) — `ANSIPalette`, `sgr`, `DecideColor`, `BackgroundFrom`.
8. <a id="source-8"></a> [Adapter](../../internal/logging/logging.go#L79-L270) — `LogLevels`, `ParseLogLevel`, `FromEnv`, `Init`, `InitWorker`, `InitFile`, `SetLogger`, `SetPalette`; logtheme/logtheme.go `Palette`, `SetBackground`; spaceview visual.go `Color.Spec`.
9. <a id="source-9"></a> [Root flags](../../cmd/yoloctl/logflags.go#L15-L81) — `addLogFlags`, `logSettings`, `renderCLIError`, `executeRoot`; main.go calls `executeRoot`.
10. <a id="source-10"></a> [Holder and chat](../../internal/session/holder.go#L229) — `agentLaunchSummary` (L642); chatui view.go `noticeRuns`, transcript.go error and delivery labels.
11. <a id="source-11"></a> [Rendering regressions](../../pkg/prettylog/render_test.go#L1-L160) — pretty/plain goldens, escaping, colour, quiet and error levels, `DecideColor` table; fold, blocking writer and interleaving in behavior_test.go.
12. <a id="source-12"></a> [Step and wire regressions](../../pkg/prettylog/behavior_test.go#L84-L397) — review1_test.go bounded empty lines, last reserved key, strict decoding, unambiguous fold keys; step lines, snapshot round trip through the wire at Quiet, monotonic states, eviction, typed round trip, caps, unrepresentable records, incomplete streams, `Route`.
13. <a id="source-13"></a> [Adapter regressions](../../internal/logging/logging_test.go#L1) — env, `SetLogger` restore, palette, no UI imports; logtheme and cmd/yoloctl logflags_test.go; chatui level_glyph_test.go; session holder_argv_test.go.
14. <a id="source-14"></a> [Host record routing](../../internal/controller/client.go#L45) — `clientLogStreams`, `HostLogSink`, `case "log"`; cmd/yoloctl stepview.go `installStepView`, controller_client.go `callControllerTerminal`.
15. <a id="source-15"></a> [Steps block](../../pkg/prettylog/stepview/stepview.go#L1) — `Apply`, `blockRows`, `summaryLocked`, `Writer`, `Suspend`, `Close`; chronology_test.go and stepview_test.go screen checks.

16. <a id="source-16"></a> [CLI routing](cli-logging.md) — command outcomes, stderr/data separation and input handoffs.

17. <a id="source-17"></a> [Structured emission](../../pkg/prettylog/emission.go#L9) — `Emission`, `EmissionSink`, `HandleOwned`, `publish`.
18. <a id="source-18"></a> [Structured emission regressions](../../pkg/prettylog/emission_test.go#L1) — owner ordering, filter/timer/flush/close, errors, byte/wire/step parity and serialized callers.

19. <a id="source-19"></a> [Line summaries](../../pkg/prettylog/step_lines.go#L1) — `groupStepLine`, `flushStepLines`; `step_lines_test.go`; stepview/failures.go `Failures.expand`; internal/logging `Init`; cmd/yoloctl logflags.go `executeRoot`.

20. <a id="source-20"></a> [Stored logs](../../cmd/yoloctl/space_log_render.go#L44) — `newLogView`, `Write`, `emit`; host_log.go `tailHostLogWithSettings`; controller_lifecycle.go `newSpaceOperationCmd`.
