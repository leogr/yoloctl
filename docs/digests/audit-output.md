# Audit event output and following

Verification: `3e04d2b43ab4c848462358f2cc74cc1845328f5f` and accompanying changes.

[FACT] `audit logs --follow` prints the first bounded-history summary to stderr,
then prints another only when collector drops, metadata gaps or retention-gap
state changes. Sliding interval times, page counts and cursors do not produce
notices. Each completed page advances the cursor even when filtering yields no
events. JSONL stdout contains complete retained events, never summaries.
[1](#source-1) [2](#source-2)

[FACT] Pretty logs show time, severity, box and rule, then the rendered pinned
Falco message. `--verbose` adds the raw retained fields, one per indented row;
JSON always includes them. Message continuations use four spaces, fields eight;
output wraps to terminal width, or 100 columns for scripts. The origin column
shows `(vm)` for Falco's literal `host` ID inside the VM, `(unknown)` for absent
IDs and `(no box)` for a container lacking box metadata. These labels describe
reported context, not per-process confinement proof. Pretty assignments with unavailable values are hidden; JSON keeps
explicit `<NA>` values. Terminal output colours severity, bolds the rule and
values, and dims field keys. Redirection, nonempty `NO_COLOR` or `TERM=dumb`
select plain text. [1](#source-1) [3](#source-3)

[FACT] The TUI always shows the selected event's message; Enter toggles its
fields. Details wrap within a bounded pane. Idle polls preserve selection and
visible events; new events append within the 500-event bound. Only the first
successful load, or a newly applied box filter, requests a history tail; an
initially empty store does not cause repeated tail reads. Status and loss/gap
changes remain visible. [3](#source-3)

[FACT] New records add `output` and `output_fields`. The latter allow-list keeps
process name/executable, user name/UID, container ID/name, file/socket name,
source/target, selected client/remote IP, remote port, parent executable,
UID syscall arguments/result and supported `yolo.*` identity fields. UID and
remote-port fields accept bounded unsigned numeric JSON as well as strings;
other fields do not gain numeric coercion.
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

## Sources

1. <a id="source-1"></a> [Host audit commands](../../cmd/yoloctl/audit.go#L280) — `streamAuditLogs`, `auditColorEnabled`, `reportAuditSummary`, `runAuditView`.
2. <a id="source-2"></a> [Bounded history](../../internal/audit/logs.go#L101) — `Store.Scan`, filtered cursor advancement.
3. <a id="source-3"></a> [Read-only view](../../internal/auditui/view.go#L1) — `Model.load`, `Model.Update`, `EventText`, `Model.View`.
4. <a id="source-4"></a> [Event schema/privacy](../../internal/audit/types.go#L1) — `Event`, `ParseEvent`, `Clean`.
5. <a id="source-5"></a> [Selected rendering](../../internal/audit/output.go#L1) — `retainedFields`, `SelectedFields`, `RenderOutput`, `FormatPrettyOutput`.
6. <a id="source-6"></a> [Pinned rules](../../internal/audit/rules.yaml#L1) — rule output templates.
7. <a id="source-7"></a> [Native toggle](../../cmd/yoloctl/controller_audit_linux.go#L1) — `audit.enable`.
8. <a id="source-8"></a> [Escape output regressions](../../internal/audit/escape_output_test.go#L1) — selected UID/network round trips, invalid numeric types and private-field exclusions.
