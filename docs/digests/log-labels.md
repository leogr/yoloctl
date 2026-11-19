# Log labels: place and actor

Verification: `c6e67053e4fd40a8987dc6be1c3f34bb4d6b1380` and accompanying changes.

[FACT] `prettylog.Options.Label` maps a root record's actor to a place and a
shown name. With a place, the pretty label is `place▸name` (place and `▸`
dim, name in the actor colour) and the plain label `place>name`; the record's
executor leaves the label and prefixes the message (`buildkit: …`, `Notice`
colour), and a `prettylog.subject` marker (`prettylog.Subject`) follows the
message in the actor colour; an ordinary `subject` field stays a field.
A nil hook or an empty place keeps `actor[ · executor]` (plain
`actor[/executor]`). Place, name, tool and subject are escaped like the
message, and wrapped text keeps the message column. [1](#source-1) [2](#source-2)

[FACT] `logging.Label` shows the CLI as `host▸yoloctl`, `vm` and `install`
under `host`, and the controller, builder, box, session, storage, audit and
chat under `space`; any other actor keeps its old label. Records, JSON and
the wire keep the actor value (`host`). `logging.Presentation` adds the hook
and the 18-cell column (the longest label, `space▸controller`, is 16) to host
stderr and its fallback, the steps block, follow views, log files, redirected
controller errors and plain audit output. [3](#source-3) [4](#source-4)

[FACT] `logging.ParseLabel` reads both plain forms back, accepting
`place>name` only when it maps back through `Label`; after it,
`logging.CutTool` takes a `tool: ` prefix only for a known tool (the
vocabulary plus `kernel`, `lima`, `ocsf`). `space logs`
and the unit-log owner check use it, so files written before and after this
release both parse. A raw line such as `>external: task` is not a label, and
an ordinary message's first word (`autoupdate: checking`) stays message text. [3](#source-3) [5](#source-5)

[FACT] A mutating routed box/account command and space lifecycle, export and
import first log the notice `using space: NAME`, once per space per outer
command; a nested execution shares the outer command's state and a new outer
command starts empty. Reads, raw exec/attach, dry runs, chat, completion and
`--all-spaces` print none. Box create's workdir notice no longer repeats the
space. At `info` on a terminal, host stderr and the steps block then hide that
space's field (`Options.HideField`); plain output, other levels and other
spaces keep it. [3](#source-3) [6](#source-6) [7](#source-7)

[FACT] Fake-writer tests cover the label shapes, fallbacks, escaping, hanging
indent, field hiding, the steps block, both parse forms, the once-per-command
rule with a nested execution, completion and routed data/raw commands. No
terminal or live VM run is implied. [2](#source-2) [7](#source-7) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Rendering](../../pkg/prettylog/format.go#L164-L389) — `shownFields`, `render`, `firstLineAppend`; `Options.Label`/`HideField` in logger.go, `Subject` in level.go.
2. <a id="source-2"></a> [Label regressions](../../pkg/prettylog/place_label_test.go#L1) — shapes, fallback, escaping, wrap indent, `HideField`; stepview_test.go `TestBlockUsesLabelAndHideField`.
3. <a id="source-3"></a> [Vocabulary and presentation](../../internal/logging/logging.go#L58-L176) — `Label`, `ParseLabel`, `CutTool`, `Presentation`, `BeginCommand`, `NameSpace`, `HideNamedSpace`.
4. <a id="source-4"></a> [Sinks](../../cmd/yoloctl/stepview.go#L33) — `installStepView`; controller_client.go, audit.go, audit_rules.go, internal/controller client.go and log_transport.go `plainLogSink`.
5. <a id="source-5"></a> [Plain readers](../../cmd/yoloctl/space_log_render.go#L109) — `spaceLogRecord`; unit_log_render.go `ownedRecordFrame`.
6. <a id="source-6"></a> [Space line](../../cmd/yoloctl/cli_logging.go#L83) — `cliUsingSpace`, `cliOperation`; controller_client.go routed RunE (L133); logflags.go outer-command reset; agentbox.go workdir notice (L1313).
7. <a id="source-7"></a> [Space line regressions](../../cmd/yoloctl/using_space_test.go#L1) — once per outer command, nested execution, completion; cli_logging_test.go `TestCLIControllerRoutingKeepsDataAndRawBytes`.
8. <a id="source-8"></a> [Parse regressions](../../internal/logging/place_test.go#L1) — mapping, both forms, `CutTool`, `HideNamedSpace`; space_log_render_test.go new-form rows.
