# Host and Lima diagnostics

Verification: `a4322a8c233da5f9eeb51239db68fab34d55f9b0` and accompanying changes.

[FACT] Lima inventory, VM create/start/stop/delete/copy and account-disk calls
capture launcher stderr, including successful warnings/debug output. They append
the last 64 KiB to `<config-root>/logs/host.log`, shared across spaces and their
Lima homes. Config-root selection follows `YOLOCTL_HOME`/XDG/platform rules.
Records keep UTC time, a quoted operation and quoted diagnostic bytes; new
records append `space="NAME"`. VM calls and interactive callbacks supply their
explicit space; global inventory/disk calls have empty context. Diagnostic text
is never used to guess a space. Quoting escapes stored terminal controls.
[1](#source-1) [2](#source-2) [3](#source-3) [9](#source-9)

[FACT] The directory is owned/private 0700, the file owned/private 0600;
symlinks are refused. Directory group is unchecked: it grants no access, and
macOS may initially inherit the parent's group before the creator changes it.
Failed logging never fails a successful operation; for a failed operation the
logging error joins its error. [2](#source-2) [5](#source-5) [15](#source-15)

[FACT] Before an append exceeds 1 MiB, `host.log` rotates to `host.log.1`,
replacing the previous generation (about 2 MiB retained). A writer locks the
opened generation and checks its current-name identity before checking
regular-file/owner/0600/single-link privacy, then appends one write. This avoids
writing into a retired inode or rejecting an unlinked waiting descriptor after
two rotations. A hard-linked/nonprivate current file remains refused. Four
attempts cover current-name races, including macOS's create/rename ENOENT.
The shared `rotatelog` appender retains file/directory sync and quoted-byte
storage. [2](#source-2) [5](#source-5) [16](#source-16) [17](#source-17)

[FACT] Failures preserve the underlying exit error and printable 2 KiB stderr
suffix. Capture follows the direct child, with bounded drain when a daemon
retains stderr; foreground stdout and caller-selected application/SSH stderr
stay explicit. No application input or PTY routing changes.
[2](#source-2) [3](#source-3) [10](#source-10)

[FACT] Listing skips absent homes and homes without instance directories before
calling limactl; files and dot/underscore infrastructure directories do not
count. Other inspection errors remain errors. Successful warnings after an
instance disappears are logged off-terminal. [4](#source-4) [5](#source-5)
[14](#source-14)

[FACT] Native chat setup/reconnect, nickname, attachment, participant polling,
channel catalogs and streams, plus space telemetry, retain private diagnostic
tails with explicit space context. Failures reach the caller/view as errors;
finite CLI calls keep their stderr destination. [6](#source-6) [7](#source-7)
[8](#source-8) [12](#source-12) [13](#source-13)

[FACT] `space logs --host` reads both private generations without contacting a
VM. Explicit `SPACE` filters by stored context; no SPACE means all records,
independent of current/default space. Older/unattributed records appear only
unfiltered. `-n` selects matching records across both files; `-f` drains the
opened file and follows its name across rotation. The history scan iterates
over the snapshot in place, preserving line endings and the final partial
line. Read/ownership errors and
writer failures remain errors; cancellation ends following.
[11](#source-11) [18](#source-18)

[FACT] Recognized records use the shared follow renderer: original time, host,
operation, optional space and the complete unquoted diagnostic body. Controls
are escaped for display. The format stores no severity, so records are info,
without guessing levels from vendor text. Unknown lines remain text. Debug
preserves selected stored bytes, including quoting; ordinary level filtering
and folding affect presentation only. `--host` and `--unit` are exclusive.
[18](#source-18) [19](#source-19)

[FACT] Scripted/process/file tests cover private captures, underlying errors,
success despite failed logging, rotation races, legacy/current parsing, space
attribution, matching tails, follow/cancel, unsafe files, complete bodies and
exact debug bytes. These are not native macOS/Lima/TUI qualification.
[5](#source-5) [8](#source-8) [18](#source-18) [19](#source-19)

## Sources

1. <a id="source-1"></a> [Layout](../../internal/layout/layout.go#L118-L210) — `HostDiagnosticsLogPath` and `ConfigRoot` selection.
2. <a id="source-2"></a> [Capture and log](../../internal/lima/diagnostics.go#L24-L204) — `LogDiagnostics`, `openLogsDirectory`, `appendHostLog`, `openHostLog` (ENOENT retry), `recordDiagnostics`, `runWithDiagnostics`.
3. <a id="source-3"></a> [Lima commands](../../internal/lima/lima.go#L31-L122) — `Create`, `runSpace`, `runDisk`.
4. <a id="source-4"></a> [Inventory](../../internal/lima/list.go#L28-L158) — `Client.List`, `homeHasInstances`.
5. <a id="source-5"></a> [Lima regressions](../../internal/lima/diagnostics_test.go#L63) — warning, skip, operation, error, daemon, planted-log, rotation, ENOENT-retry and logs-directory mode tests.
6. <a id="source-6"></a> [Interactive helpers](../../cmd/yoloctl/chat_stream_stderr.go#L26-L107) — `callChatStream`, `callChatViewControllerInto`, `stderrTail.finishSpace`; its callers are `chat_open.go:38` and `chat_view.go:86,416,662`.
7. <a id="source-7"></a> [Telemetry](../../cmd/yoloctl/space_view.go#L147-L191) — `streamSpaceView`.
8. <a id="source-8"></a> [Chat regressions](../../cmd/yoloctl/chat_view_diagnostics_test.go#L17-L57) — interactive methods, success/error diagnostics and terminal sinks.
9. <a id="source-9"></a> [Disk inspection](../../internal/lima/disk.go#L21-L40) — `AccountDisk`.
10. <a id="source-10"></a> [Explicit streaming](../../internal/lima/stderr.go#L68-L157) — caller-owned application stderr.
11. <a id="source-11"></a> [Log-view command](../../cmd/yoloctl/space.go#L53-L87) — `newSpaceLogsCmd`.
12. <a id="source-12"></a> [Catalog regressions](../../cmd/yoloctl/chat_catalog_diagnostics_test.go#L38-L116) — real controller frames and private concurrent tails.
13. <a id="source-13"></a> [Chat callbacks](../../cmd/yoloctl/chat_view.go#L86-L712) — nickname, participant polling (`agentsLoop`) and attach (`resolveAttach`); setup/reconnect use `openChatHeader` in `chat_open.go:38`.
14. <a id="source-14"></a> [Lima v2.2.1 instance enumeration](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/store/store.go#L47-L70) — directory-only, dot/underscore exclusions.
15. <a id="source-15"></a> [Stop regression](../../cmd/yoloctl/lima_log_failure_stop_test.go#L1-L58) — `stopSpaceRuntime` with a planted log; `RecordSpaceDiagnostics` callers in `chat_stream_stderr.go` and `space_view.go`.

16. <a id="source-16"></a> [Stale-generation regression](../../internal/lima/diagnostics_test.go#L333) — `TestHostDiagnosticsReopensUnlinkedGeneration` uses two production rotations, a retained unlinked descriptor and current hardlink/mode negatives; `appendLocked` in internal/rotatelog/append.go checks identity before current-file validation.

17. <a id="source-17"></a> [Shared appender](../../internal/rotatelog/append.go#L35) — `Append`, `appendLocked`, `rotate`; `internal/lima/diagnostics.go` `hostLogAppender`, `appendHostLog` delegation.

18. <a id="source-18"></a> [Host log reader](../../internal/lima/hostlog.go#L25) — `ParseHostDiagnostic`, `TailHostDiagnostics`, `hostLogSnapshot`, `followHostLog`; hostlog_test.go history/filter/rotation/privacy controls; rotatelog/read.go `OpenRead`. Snapshot line iteration verified at `4c8feb32ca543724e57161ebf97afb44e7748ccb` and accompanying changes.
19. <a id="source-19"></a> [Host view](../../cmd/yoloctl/host_log.go#L1) — `hostLogRecord`, `tailHostLogWithSettings`; host_log_test.go command, complete-body, exact-debug and failure controls.
