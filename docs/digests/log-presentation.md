# Log presentation

Verification: `56a57bcd079bcc8733667b9890b6796f911c6fac` and accompanying changes.

[FACT] `pkg/prettylog` is a public package of the yoloctl module that renders
`log/slog` records. It imports no yoloctl or UI package and reads no terminal.
A record's presentation kind comes from one shared parse of its reserved
top-level attributes: only the last occurrence of a reserved key counts, and
if its value has the wrong type the key is absent and that value is an ordinary
field (earlier occurrences are ignored everywhere, including on the wire). An
`outcome` or step `state` decides first, then the level. Each
kind has one glyph (`✗ ! • › ✓ ? · ○`) and one plain prefix (`error:`,
`warning:`, `notice:`, `uncertain:`, `debug:`). [1](#source-1) [2](#source-2)

[FACT] Pretty lines are `glyph actor[ · executor]  message  fields  took`;
plain lines are `[time ]prefix actor[/executor]: message fields (took)`,
keeping the message's case. Controls in messages, keys, group names and values
render as `\xNN`; a message newline becomes an aligned continuation line. An
`err` attribute is shown with its record at every verbosity, as a continuation
line or `: err`. Display levels: `error` shows errors and uncertain results
(failed and unknown outcomes included); `warn` adds warnings and every final
outcome; `info` (default) adds notices and progress; `debug` adds debug records
and step detail lines.
[3](#source-3) [4](#source-4) [11](#source-11)

[FACT] A `Logger` is synchronous: a record is written before `Handle`
returns, one mutex serializes every derived handler, write errors are
remembered and never change results, and the writer is never closed.
Consecutive identical records (same level, message and typed attributes, under
a length-prefixed key) within the fold window print once, followed by
`↳ repeated N more times until HH:MM:SS` when the run ends (different record,
timer, `Flush` or `Close`); outcomes, steps and details never fold. `Route`
sends step records to a sink and other lines to another writer. [4](#source-4)
[11](#source-11)

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
level is an error, and `-v` stays Cobra's `--version` shorthand. `main` renders an unsilenced command error once
through the `renderCLIError` hook (by default Cobra's `Error:` line) and closes
the Logger. [8](#source-8) [9](#source-9) [13](#source-13)

[FACT] Relayed worker records go to the process Logger when the command's
stderr is the process stderr, else as plain lines to that writer; a rendering
error is reported once without changing the result. On a terminal at the
default `info` level, `stepview` draws running root steps as a block cut to the terminal (no
terminal reads or keys), leaves one line per ended root plus a failed
sub-step's last 20 lines, defers output while suspended and summarizes a
replayed snapshot after its end record. It closes before the command's final
error, showing a root step still running as `?` "did not finish before the
command ended". Without a block, below debug, a failed relayed step's last 20
lines follow as error records split under the message limit (the heading cuts
the step title to 120 bytes); their write
errors are reported like the step's. [14](#source-14) [15](#source-15)

[FACT] The holder logs an agent launch as its program name and argument count,
never its argv. Chat notices and delivery marks use the shared `✗`/`?` glyphs
and the theme's `Warn` tone. [10](#source-10)

## Sources

1. <a id="source-1"></a> [Kinds and glyphs](../../pkg/prettylog/level.go#L104-L176) — `KindOf`, `kindOf`, `DefaultGlyphs`, `PlainPrefix`.
2. <a id="source-2"></a> [Reserved parse](../../pkg/prettylog/structure.go#L26-L181) — `StructureOf`, `eachAttr`, `validReserved`, `apply`, `IsLossy`.
3. <a id="source-3"></a> [Line grammar](../../pkg/prettylog/format.go#L50-L405) — `escape`, `cut`, `fieldsText`, `render`, `renderPlain`, `summary`.
4. <a id="source-4"></a> [Logger](../../pkg/prettylog/logger.go#L43-L447) — `Verbosity`, `allows`, `wireAllows`, `New`, `Route`, `Close`, `emit`, `arm`, `endRun`, `handler`.
5. <a id="source-5"></a> [Steps and Tracker](../../pkg/prettylog/tracker.go#L71-L311) — `Apply`, `restore`, `lineCost`, `addLine`, `victim`, `Snapshot`; step.go `StartStep`, `Detail`, `end`.
6. <a id="source-6"></a> [Wire](../../pkg/prettylog/wire.go#L19-L328) — `Opaque`, `MarshalRecord`, `UnmarshalRecord`, `atEOF`, `writeWire`, `WireReader.Next`.
7. <a id="source-7"></a> [Colour](../../pkg/prettylog/color.go#L38-L140) — `ANSIPalette`, `sgr`, `DecideColor`, `BackgroundFrom`.
8. <a id="source-8"></a> [Adapter](../../internal/logging/logging.go#L79-L252) — `LogLevels`, `ParseLogLevel`, `FromEnv`, `Init`, `InitWorker`, `InitFile`, `SetLogger`, `SetPalette`; logtheme/logtheme.go `Palette`, `SetBackground`; spaceview visual.go `Color.Spec`.
9. <a id="source-9"></a> [Root flags](../../cmd/yoloctl/logflags.go#L15-L81) — `addLogFlags`, `logSettings`, `renderCLIError`, `executeRoot`; main.go calls `executeRoot`.
10. <a id="source-10"></a> [Holder and chat](../../internal/session/holder.go#L229) — `agentLaunchSummary` (L642); chatui view.go `noticeRuns`, transcript.go error and delivery labels.
11. <a id="source-11"></a> [Rendering regressions](../../pkg/prettylog/render_test.go#L1-L160) — pretty/plain goldens, escaping, colour, quiet and error levels, `DecideColor` table; fold, blocking writer and interleaving in behavior_test.go.
12. <a id="source-12"></a> [Step and wire regressions](../../pkg/prettylog/behavior_test.go#L84-L397) — review1_test.go bounded empty lines, last reserved key, strict decoding, unambiguous fold keys; step lines, snapshot round trip through the wire at Quiet, monotonic states, eviction, typed round trip, caps, unrepresentable records, incomplete streams, `Route`.
13. <a id="source-13"></a> [Adapter regressions](../../internal/logging/logging_test.go#L1) — env, `SetLogger` restore, palette, no UI imports; logtheme and cmd/yoloctl logflags_test.go; chatui level_glyph_test.go; session holder_argv_test.go.
14. <a id="source-14"></a> [Host record routing](../../internal/controller/client.go#L45) — `clientLogStreams`, `HostLogSink`, `case "log"`; cmd/yoloctl stepview.go `installStepView`, controller_client.go `callControllerTerminal`.
15. <a id="source-15"></a> [Steps block](../../pkg/prettylog/stepview/stepview.go#L1) — `Install`, `Apply`, `blockRows`, `summaryLocked`, `Writer`, `Suspend`, `Close`; failures.go `Failures`; cmd/yoloctl logflags.go `executeRoot` order; stepview_test.go screen regressions.
