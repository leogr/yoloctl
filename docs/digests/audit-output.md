# Audit event output and following

Verification: `46da38583fa61bae0ff7138f03b557872d027e9e` and accompanying changes.

[FACT] `audit logs --follow` prints the first bounded-history summary to stderr,
then prints another only when collector drops, metadata gaps or retention-gap
state changes. Sliding interval times, page counts and cursors do not produce
notices. Each completed page advances the cursor even when filtering yields no
events. JSONL stdout contains complete retained events, never summaries.
[1](#source-1) [2](#source-2)

[FACT] Human-readable logs use the shared glyphs and palette roles, preserving
Falco's priority word, origin and bold rule. The next indented block reconstructs
the pinned rule message; `--verbose` adds retained fields with aligned key/value
columns. Wrapping uses four-space message continuations and eight-space field
rows, terminal width or 100 columns for scripts. Origins remain `(vm)` for the
literal `host` ID, `(unknown)` for no ID and `(no box)` for an unjoined container;
none is proof of per-process confinement. `<NA>` assignments stay hidden in
pretty output and retained in JSON. `--log-level`/`-q` filter only presentation;
`--json` and exports keep all selected retained events. `--color` follows the
shared policy: automatic colour only on terminals unless `NO_COLOR` or
`TERM=dumb`; an explicit policy overrides it. Line-only commands never query
the terminal. The TUI owns its background request and supplies the shared
space-view palette after a reply; until then it uses ANSI16 plus faint.
[1](#source-1) [3](#source-3) [9](#source-9) [10](#source-10) [14](#source-14)

[FACT] One prettylog engine folds only consecutive matching alerts within ten
seconds. Identity comprises priority, rule, reported origin, executable, result,
path and endpoint, never time/cursor or arbitrary raw output. The first record
appears immediately; repeats yield a separate `↳ repeated N more times until
HH:MM:SS` line on the timer, next different shown record, flush or close. There
is no redraw or multi-key aggregation: A B A stays three records. JSON, storage
and export never fold. A read failure or exit still closes the presentation
stream and drains its pending summary. Writer errors remain errors, without
retry. [9](#source-9) [11](#source-11) [12](#source-12) [15](#source-15)

[FACT] The TUI retains at most 500 displayed logical rows and bounded snapshots,
not a second raw-event backlog. Each summary owns its first displayed event's
immutable details, even if that event's row was evicted; an unknown/fully evicted
owner never resolves to a newer row. Enter shows aligned fields, with an explicit
first-event label for a summary. Timer callbacks only retain rows and signal a
one-slot wake; the Tea update path controls selection. Idle polls preserve
selection. Filter reset closes the old stream and fences old loads/wakes;
view exit or context cancellation closes pending timers. Only initial or newly
filtered history requests a tail. The status band keeps service/errors and
loss/gap state visible independently of event verbosity.
[3](#source-3) [11](#source-11) [13](#source-13) [16](#source-16)

[FACT] New records add `output` and `output_fields`. The latter allow-list keeps
process name/executable, user name/UID, container ID/name, file/socket name,
source/target, selected client/remote IP, remote port, parent executable,
UID syscall arguments/result and supported `yolo.*` identity fields. UID and
remote-port fields accept bounded unsigned numeric JSON as well as strings;
other fields do not gain numeric coercion. The UID condition
uses `evt.args` only for matching; aggregate arguments are never persisted.
Retained `evt.arg.*` values remain resolved names/placeholders.
Selected values share a 4 KiB budget; individual strings are escaped and capped
at 1024 bytes. The stored message is capped at 1024 bytes and reconstructed
from the pinned template and selected fields. Arbitrary incoming `output`,
argv, environment, aggregate labels and buffers are not persisted. Unavailable
metadata clears identity claims rather than retaining stale plugin values.
[4](#source-4) [5](#source-5) [8](#source-8)

[FACT] Existing records remain readable: pretty messages reconstruct the current
pinned template from their older flattened fields. Missing process/user/name
fields cannot be recovered from history. Updated rule outputs request the new
fields for subsequent collection; explicit audit enable selects the updated
rules digest. [4](#source-4) [5](#source-5) [6](#source-6) [7](#source-7)

[FACT] For unknown rule names the same renderer uses a fixed selected-field
message. It never interprets imported output templates; raw argv/environment,
aggregate arguments and arbitrary fields remain excluded. A custom rule reusing
a shipped name keeps that shipped name's template, potentially different from
its changed semantics. [5](#source-5)

## Sources

1. <a id="source-1"></a> [Host audit commands](../../cmd/yoloctl/audit.go#L280) — `streamAuditLogs`, `auditSummaryText`, `runAuditView`.
2. <a id="source-2"></a> [Bounded history](../../internal/audit/logs.go#L101) — `Store.Scan`, filtered cursor advancement.
3. <a id="source-3"></a> [Read-only view](../../internal/auditui/view.go#L1) — `Model.load`, `Model.Update`, `Model.visible`, `Model.View`, `Model.Close`.
4. <a id="source-4"></a> [Event schema/privacy](../../internal/audit/types.go#L1) — `Event`, `ParseEvent`, `Clean`.
5. <a id="source-5"></a> [Selected rendering](../../internal/audit/output.go#L1) — `retainedFields`, `SelectedFields`, `RenderOutput`, `FormatPrettyOutput`.
6. <a id="source-6"></a> [Pinned rules](../../internal/audit/rules.yaml#L1) — rule output templates.
7. <a id="source-7"></a> [Native toggle](../../cmd/yoloctl/controller_audit_linux.go#L1) — `audit.enable`.
8. <a id="source-8"></a> [Escape output regressions](../../internal/audit/escape_output_test.go#L1) — selected UID/network round trips, invalid numeric types and private-field exclusions.
9. <a id="source-9"></a> [Domain projection](../../internal/auditui/logging.go#L17) — `eventLevel`, `eventOrigin`, `eventKey`, `eventRecord`.
10. <a id="source-10"></a> [Domain rendering](../../internal/auditui/render.go#L33) — `newEventRenderer`, `rolePrefix`, heading/message/fields and summary.
11. <a id="source-11"></a> [Bounded presentation owner](../../internal/auditui/stream.go#L27) — `rowSink.Emit`, `eventFromRecord`, `newEventStream`, `add`, `close`.
12. <a id="source-12"></a> [Line presentation](../../internal/auditui/printer.go#L19) — `printSink.Emit`, `NewLogPrinter`.
13. <a id="source-13"></a> [Display regressions](../../internal/auditui/display_test.go#L19) — summary ownership, filter/wake/close, theme, consecutive streams and writer errors.
14. <a id="source-14"></a> [Theme bridge](../../internal/logging/logtheme/logtheme.go#L18) — `Palette`, `SetBackground`.
15. <a id="source-15"></a> [Shared emission](../../pkg/prettylog/emission.go#L15) — typed record/summary decisions from `Logger.emitOwned`/`endRun`.
16. <a id="source-16"></a> [CLI regressions](../../cmd/yoloctl/audit_logging_test.go#L46) — flags, unchanged JSON and failure drain.
