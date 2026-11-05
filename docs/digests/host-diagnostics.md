# Host and Lima diagnostics

Verification: `8931e8df3abebc5c0fdd6f77bd473110a2e7d23b` and accompanying changes.

[FACT] The Lima Client’s inventory, VM create/start/stop/delete/copy and account-disk commands
capture launcher stderr, including successful warnings and debug output. They
append the last 64 KiB per invocation to `<config-root>/logs/host.log`; the
config root follows the ordinary `YOLOCTL_HOME`/XDG/platform selection. The log
is shared across homes because inventory spans spaces. Entries contain a UTC
timestamp, operation label and quoted diagnostic bytes; terminal controls are
escaped. The directory is owned and private (0700), the file owned and private
(0600); symlinks are refused. An append failure becomes a returned error,
including when the command completed successfully. [1](#source-1) [2](#source-2)
[3](#source-3) [9](#source-9)

[FACT] Command failures retain the underlying exit error and include a bounded,
printable 2 KiB stderr suffix. Capture completion follows the direct child,
with a bounded drain when a daemon retains stderr. Foreground command stdout
is preserved. Explicit application/SSH passthrough stderr still uses its
caller-selected stream; this capture does not change application input or PTY
routing. [2](#source-2) [3](#source-3) [10](#source-10)

[FACT] Listing skips absent homes and homes without instance directories before
running limactl. Files and dot/underscore infrastructure directories do not
count as instances. Other filesystem inspection errors remain errors. If an
instance disappears after that check and limactl emits a successful warning,
the warning is still logged without writing the terminal. [4](#source-4)
[5](#source-5) [14](#source-14)

[FACT] Native chat's initial/reconnect header, nickname, attach resolution,
participant polling, channel catalogs and stream use private diagnostic tails.
Space telemetry does too. Their captured diagnostics are logged; failures reach
the caller/view through returned errors, rather than raw terminal writes.
Finite chat CLI calls keep their explicit stderr writer. `space logs` still
streams the VM boot/runtime log; its help points to this separate host log.
[6](#source-6) [7](#source-7) [8](#source-8)
[11](#source-11) [12](#source-12) [13](#source-13)

[FACT] Fake-command and controller tests verify silent terminal sinks, logged
success warnings, error context/exit status, debug capture, empty-home skips,
log-link refusal and direct-child completion. They establish scripted coverage,
not native macOS/Lima/TUI runtime validation. [5](#source-5) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Layout](../../internal/layout/layout.go#L118-L210) — `HostDiagnosticsLogPath` and `ConfigRoot` selection.
2. <a id="source-2"></a> [Capture and log](../../internal/lima/diagnostics.go#L24) — `LogDiagnostics`, `runWithDiagnostics`.
3. <a id="source-3"></a> [Lima commands](../../internal/lima/lima.go#L31-L122) — `Create`, `runSpace`, `runDisk`.
4. <a id="source-4"></a> [Inventory](../../internal/lima/list.go#L28) — `Client.List`, `homeHasInstances`.
5. <a id="source-5"></a> [Lima regressions](../../internal/lima/diagnostics_test.go#L63) — warning, skip, operation, error, daemon and log tests.
6. <a id="source-6"></a> [Interactive helpers](../../cmd/yoloctl/chat_stream_stderr.go#L26) — `callChatStream`, `callChatViewControllerInto`, `stderrTail.finish`; its callers are `chat_open.go:38` and `chat_view.go:86,416,662`.
7. <a id="source-7"></a> [Telemetry](../../cmd/yoloctl/space_view.go#L147-L191) — `streamSpaceView`.
8. <a id="source-8"></a> [Chat regressions](../../cmd/yoloctl/chat_view_diagnostics_test.go#L17) — interactive methods, success/error diagnostics and terminal sinks.
9. <a id="source-9"></a> [Disk inspection](../../internal/lima/disk.go#L21-L40) — `AccountDisk`.
10. <a id="source-10"></a> [Explicit streaming](../../internal/lima/stderr.go#L68-L146) — caller-owned application stderr.
11. <a id="source-11"></a> [VM log help](../../cmd/yoloctl/space.go#L58) — `newSpaceLogsCmd`.
12. <a id="source-12"></a> [Catalog regressions](../../cmd/yoloctl/chat_catalog_diagnostics_test.go#L38) — real controller frames and private concurrent tails.
13. <a id="source-13"></a> [Chat callbacks](../../cmd/yoloctl/chat_view.go#L86) — nickname, participant polling (`agentsLoop`) and attach (`resolveAttach`); setup/reconnect use `openChatHeader` in `chat_open.go:38`.
14. <a id="source-14"></a> [Lima v2.2.1 instance enumeration](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/store/store.go#L47-L70) — directory-only, dot/underscore exclusions.
