# Background log storage and views

Verification: `ad96d0605c1f5d3bac1b075af4c399021ee86ad9` and accompanying changes.

[FACT] The controller service, managed holder and Firecracker guest init use the
shared file Logger: plain UTC RFC3339 timestamps, levels and actor fields, no
colour, consecutive-record folding. Operator restore/serve/shutdown, deferred
maintenance, host-storage pause/recovery and VM-owned diagnostics use that
Logger. The holder still logs only
the agent program and argument count. These are diagnostic streams, never
operation receipts or proof of success. [1](#source-1) [2](#source-2)

[FACT] Private append logs share the host diagnostics rotation algorithm. Each
append locks the opened generation, checks its identity against the current
name before checking owner/0600/regular-file/single-link privacy, then writes or
rotates and reopens. Four attempts bound name races. Rotation keeps one `.1`
generation and syncs the directory; the existing host log additionally retains
its per-record file/directory sync and quoted-byte format. A stream Writer
splits large writes into at most 64 KiB chunks (or the smaller generation cap).
An oversized old file is replaced with its bounded suffix before normal
appends. No pathname truncation of a live inode is used. [3](#source-3)
[4](#source-4)

[FACT] Firecracker `vm.log` keeps up to 4 MiB per generation; managed guest
unit logs under `/var/log/yoloctl-units` and existing Firecracker
`prepare-*.log` files keep up to 1 MiB each plus one previous generation.
The prepare log remains in its existing cache directory, with its existing
role; no additional stored image-build log is introduced. Lima-owned runtime
logs and journald retain their existing owners and retention policies.
[5](#source-5) [6](#source-6) [7](#source-7)

[FACT] External child bytes are copied unchanged through a real inherited pipe
owned by the long-lived helper/supervisor. No CLI-side exec copier prolongs a
short-lived launcher. Closing the parent's copy does not stop a descendant's
writer. After confirmed producer retirement the receiver drains to EOF; a
unit supervisor retains the relay for an unknown surviving producer. The VM
helper instead exits after its existing bounded retirement wait; its final error
is queued before that wait, with one early-return fallback and no duplicate
record. It does not join an unknown producer or force-close the reader. The
relay cannot outlive the helper process; data still pending at exit can be lost
if the sink stalls for the whole wait. A raw sink write failure is
remembered but does not change the child result or cause a logging-induced
SIGPIPE; subsequent writes still try. Helper records enter the same raw pipe,
so a copied chunk cannot insert them inside a complete small grow-marker write.
Pipe acceptance does not acknowledge persistence; the relay retains disk write
errors in `Writer.Err`. Disk/sink failures can lose diagnostics,
and normal rotation deliberately expires old history. Direct file handlers
receive write errors; captured helper handlers observe their pipe write
instead. Logging is synchronous and adds no disk-I/O timeout.
[4](#source-4) [5](#source-5) [6](#source-6)

[FACT] Vendor output, `YOLOCTL_VM_READY`, disk-grow confirmation markers and the
controller supervisor's episode counters retain their raw formats. Disk-grow
settlement reads the last 4 MiB across both log generations, including a marker
split at rotation; it does not extend that historical search window.
[5](#source-5) [8](#source-8)

[FACT] `space logs` renders its existing VM-log output through the shared follow
Logger and continues using `tail -F` for follow-by-name. Partial input lines are
bounded at 64 KiB. Recognized plain prefixes supply presentation hints only;
the lossy file grammar cannot reconstruct typed outcomes or fields. Controls
are escaped in ordinary display and consecutive records fold.
`--log-level debug` preserves the original bytes on stdout. `warn`/`-q` hide ordinary/vendor text and blank
lines; `error` also hides warnings but retains uncertain results. The file
logger remains an internal full-diagnostic sink. These views never drive
lifecycle settlement.
[9](#source-9)

[FACT] Regressions cover raw suffixes/two rotations, an oversized existing file,
unsafe current/history files, sink recovery, a real external producer, marker
interleaving with a bypass negative control, and
stdio drain with a child still holding the pipe. Presentation tests cover
partial/control bytes, folding and exact trace output. These are isolated
filesystem/process tests; native VM/daemon behaviour needs separate runtime QA.
[10](#source-10) [11](#source-11)

## Sources

1. <a id="source-1"></a> [Process bindings](../../cmd/yoloctl/controller_internal.go#L311) — `serveController`; `session_hold.go` `newSessionHoldCmd`; `internal/vmguest/init_linux.go` `Run`; `internal/logging/logging.go` `InitFile`, `NewFileLogger`.
2. <a id="source-2"></a> [Controller notices](../../cmd/yoloctl/controller_operator.go#L75) — `restoreOperatorEndpoints`, `serveOperatorEndpoints`; `controller_storage.go` `scheduleStorageGC`; `controller_codex_observe.go` `scheduleCodexObserve`; `internal/session/holder.go` agent launch summary.
3. <a id="source-3"></a> [Append protocol](../../internal/rotatelog/append.go#L35) — `Append`, `appendLocked`, `rotate`; `internal/lima/diagnostics.go` `hostLogAppender`, `appendHostLog`.
4. <a id="source-4"></a> [Stream writer](../../internal/rotatelog/writer.go#L37) — `Open`, `Write`, `Err`, `BestEffort`; `pipe.go` `Pipe`.
5. <a id="source-5"></a> [VM helper](../../internal/firecracker/helper_linux.go#L169) — `Helper`; `helper_log.go` `helperLogFinalizer`; `driver_linux.go` `Start`; `internal/rotatelog/startup.go` `OpenStartupFile`; `stdio_linux.go` `CaptureStdio`.
6. <a id="source-6"></a> [Unit supervisor](../../internal/vmguest/units_linux.go#L130) — `managedUnit.start`, `watch`, `cleanGroup`.
7. <a id="source-7"></a> [Existing preparation log](../../internal/firecracker/prepare_linux.go#L60) — `prepareLocalImage`, `assembleLocalImage` progress callback in `assemble_linux.go`; `host_storage_linux.go` `hostStorageGuard.step`, `watch`; Lima `limaDriver.LogPath` in `internal/vm/lima.go`.
8. <a id="source-8"></a> [Raw grow settlement](../../internal/firecracker/datadisk_linux.go#L188) — `DataDiskGrowLogged`; `internal/rotatelog/read.go` `ReadTail`; `internal/vmguest/controller_linux.go` supervisor `logf`; `init_linux.go` ready/grow output.
9. <a id="source-9"></a> [Log view](../../cmd/yoloctl/space_log_render.go#L17) — `spaceLogOutput`, `spaceLogRecord`; `space.go` `tailLimaLog`.
10. <a id="source-10"></a> [Rotation regressions](../../internal/rotatelog/writer_test.go#L35) — raw producer, bounds, privacy, marker; `stdio_linux_test.go` `TestCaptureStdioDrainsBeforeHelperExit`; `marker_test.go` shared ingress and sink-failure controls; `internal/lima/diagnostics_test.go` host concurrency/stale-generation cases.
11. <a id="source-11"></a> [View and grow regressions](../../cmd/yoloctl/space_log_render_test.go#L25) — view tests; `internal/firecracker/datadisk_linux_test.go` `TestDataDiskGrowLoggedAcrossRotation`; `internal/firecracker/helper_log_test.go` final-record ordering/early-return/once controls; `controller_storage_test.go` scheduled diagnostics.
