# CLI output under the steps block

Verification: `dc857939b14bace057ca9c009367a0672facbaa3` and accompanying changes.

[FACT] Prolonged space-lock contention starts a host step with the existing
holder/advisory wording. Acquisition ends it successfully; a returned error
ends it failed. Uncontended locks stay silent. Lock timing, cancellation and
returned errors are unchanged. [1](#source-1)

[FACT] While the steps block is on the process terminal, a VM-side
`controller-internal` command's stderr (for example `space export --stop`'s
workload stop) goes through it: a line framed as a yoloctl record is shown again
through the process Logger above the block, any other line unchanged above it,
one complete line at a time. Without the block those bytes are unchanged.
Container start/stop/rm, detached run and rmi never show their stdout, which
only echoes an ID or digest; their stderr is unchanged. Pull keeps its stdout,
where pinned nerdctl writes its progress.
[2](#source-2) [3](#source-3)

[FACT] Only with the default-info interactive step view, space start's
restart callback owns a scoped child step. Its diagnostic stdout/stderr enter
separate line buffers then the existing detail writer; final partial lines
flush before that step ends. Typed worker records retain `HostLogSink`, not the
replaced error writer. Plain/debug streams and records, exec/attach/data,
controller results and lifecycle ordering are unchanged. No npm banner parsing
or durable maintenance log is added. [4](#source-4)

## Sources

1. <a id="source-1"></a> [Lock-wait progress](../../cmd/yoloctl/space_operation.go#L25) — `acquireSpaceOperationLock`; space_lock_progress_test.go exercises real lock acquisition, cancellation and uncontended silence.
2. <a id="source-2"></a> [Child stderr under the block](../../cmd/yoloctl/infra_stderr.go#L1) — `infraStderr`, `infraLines`; controller_deploy.go `nativeInfrastructure`; cli_logging.go `cliOperation`; infra_stderr_test.go `TestInfraStderrGoesThroughTheStepsBlock`, `TestCLIOperationLeavesOneFinalLineUnderTheStepsBlock`.
3. <a id="source-3"></a> [ID echoes](../../internal/container/container.go#L927) — `nerdctlEcho`, `Run` (detached), `Start`, `Stop`, `ImageRemove`, `Remove` (`ImagePull` keeps stdout); stop_id.go, remove_id.go; echo_output_test.go `TestNerdctlIDEchoesAreNotShown`.
4. <a id="source-4"></a> [Restart diagnostics](../../cmd/yoloctl/controller_restart_output.go#L16) — `restartSessionOutput`, `restartDetailStream`; controller_sessions.go `hostSessionRestartOffer`; controller_restart_output_test.go framed ownership/plain/debug/pinned-VT controls.
