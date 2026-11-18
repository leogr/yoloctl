# CLI log routing and outcomes

Verification: `dc857939b14bace057ca9c009367a0672facbaa3` and accompanying changes; prior `4580b43c30412beb0d7f57e8a4a6ef9833ef82c7` and accompanying changes. Interrupted outcomes and source symbols re-read at `eddfb42a14f31c9f8d85069f2a861931367da86d`.

[FACT] CLI warnings, notices and mutation-result prose use the process
`internal/logging` Logger; worker processes send records and the host renders
them. Lower-level box/OpenShell/sandbox warnings use the installed slog
default without importing the UI or renderer into the native helper closure.
Human account, copy, reconcile, mount and storage-result prose therefore goes
to stderr. List/show tables, JSON, dry-run plans and generated instructions
retain their data writers. Dry-run policy/image warnings are separate log
records, including their continuation text. [1](#source-1) [2](#source-2)

[FACT] Create emits the ephemeral-workdir notice after image warming and
operation admission, including manifest-only admission. Preparation re-entry
does not repeat it. [10](#source-10)

[FACT] Prolonged space-lock contention starts a host step with the existing
holder/advisory wording. Acquisition ends it successfully; a returned error
ends it failed. Uncontended locks stay silent. Lock timing, cancellation and
returned errors are unchanged. [11](#source-11)

[FACT] A routed mutation reports its final outcome only after its controller
call returns, including receipt finalization. Intermediate worker prose is not
an OK outcome. Known failures retain their text and status; typed lost replies,
interrupted image jobs and remote `interrupted`/`uncertain`/`command-uncertain`
states end as a failed outcome, "Operation interrupted" (`cliUncertain`). The
latter keeps its existing query guidance without adding a hold or
acknowledgment instruction. A remote error's own successful rendered-outcome
receipt can suppress a duplicate; an unrelated earlier `LastOutcome` cannot.
Error reasons use the reserved `err` attribute, independently of the progress
message cap. At `--log-level error`, failed/unknown outcomes remain visible
while successful outcomes are hidden. Sink failure changes neither operation status nor exit classification.
[1](#source-1) [3](#source-3) [4](#source-4)

[FACT] Host create/start/stop/recreate/destroy and archive commands report
existing phases as steps and end with an outcome after cleanup; under the
interactive steps block a successful operation's root summary is its only final
line (no second success outcome), while failures keep their outcome. Nested phases
inherit the root's parent; nested lifecycle calls do not emit another command
outcome. VM image preparation, disk allocation, boot and controller deployment
use the existing functions and guards, without new effect permission or retry.
Lima lifecycle launcher stdout/stderr feed the current phase’s detail; explicit stdout
consumers retain their writers. Owned pipes retain bounded private host-log
tails without waiting for descendant-held FDs. [9](#source-9)
[1](#source-1) [5](#source-5)

[FACT] While the steps block is on the process terminal, a VM-side
`controller-internal` command's stderr (for example `space export --stop`'s
workload stop) goes through it: a line framed as a yoloctl record is shown again
through the process Logger above the block, any other line unchanged above it,
one complete line at a time. Without the block those bytes are unchanged.
Container start/stop/rm, detached run and rmi never show their stdout, which
only echoes an ID or digest; their stderr is unchanged. Pull keeps its stdout,
where pinned nerdctl writes its progress.
[14](#source-14) [15](#source-15)

[FACT] A command report or notice aimed at the process stderr is a log record:
`cliReportTo` (chat export failures and its per-channel report, which goes to
stderr for file and stdout targets alike) and `cliNoticeSink` (image refresh and
reconcile rebuild notices) write a record there and a plain line to any other
caller writer; the post-autoupdate hint is one notice whose commands are
continuation lines. `cliLogLines` splits a lead and its listing lines over as
many records as the message limit needs, never cutting a line. [16](#source-16)

[FACT] Only with the default-info interactive step view, space start's
restart callback owns a scoped child step. Its diagnostic stdout/stderr enter
separate line buffers then the existing detail writer; final partial lines
flush before that step ends. Typed worker records retain `HostLogSink`, not the
replaced error writer. Plain/debug streams and records, exec/attach/data,
controller results and lifecycle ordering are unchanged. No npm banner parsing
or durable maintenance log is added. [17](#source-17)

[FACT] Prompts, editors, host SSH and standalone attachments suspend the shared
inline step view and resume after returning terminal ownership. They add no
input reader. Exec/attach/chat-stream output and dry-run data bypass added
command outcomes; their stream bytes are not parsed as logs. A signalled attach
client retains its separate notice and status 75, never a mutation-uncertainty
label. [1](#source-1) [3](#source-3) [6](#source-6)

[FACT] Installer warning/result records use the shared logger; generated shell
instructions stay on their data writer without embedded palette escapes.
Privileged/operator authority explanations use a visible warning's `err`
attribute so truncating a progress message cannot hide that explanation.
[7](#source-7) [8](#source-8)

[FACT] Focused tests exercise outcome-after-response, cleanup failure after
successful nested steps, typed uncertainty, sink failure, remote display
receipts, raw/JSON bytes through the real command router and real dry-run
warning separation. These simulated fixtures do not qualify a live VM,
BuildKit run or native terminal. [4](#source-4)

[FACT] Image refresh/remedy prose and verified stale-VM metadata cleanup use
builder/host records. Clipboard-policy notices during direct attachments and
controller terminal calls are session warnings. Only diagnostics pass through
the raw-terminal line adapter; session/clipboard stdout retains its byte path.
Suppressed warnings add no blank line. A caller-selected error writer receives
plain notices instead of the process renderer. The OSC 52 policy and disabled
notice's existing once-per-attachment rule are unchanged.
[12](#source-12) [13](#source-13)

## Sources

1. <a id="source-1"></a> [CLI logging helpers](../../cmd/yoloctl/cli_logging.go#L17) — `cliLog`, `renderLoggedCLIError`, `cliOperation`, `cliFinishOutcome`, `cliRoutedOutcome`, `wrapHostLogCommand`, `suspendCLISteps`.
2. <a id="source-2"></a> [Reconcile output](../../cmd/yoloctl/agentbox_reconcile.go#L209) — `printDryRun`, `printReconcileResult`; account.go command results; internal/openshell/helper_pins.go `existingHelperNotice`.
3. <a id="source-3"></a> [Controller routing](../../cmd/yoloctl/controller_client.go#L115) — `routeControllerCommands`, `reportClientDetached`; internal/controller/client.go `ExitError.State`, `OutcomeRendered`.
4. <a id="source-4"></a> [CLI regressions](../../cmd/yoloctl/cli_logging_test.go#L47) — finalization, error identity/status, uncertainty, sink rejection, JSON/raw command routing and warning separation.
5. <a id="source-5"></a> [Host lifecycle](../../cmd/yoloctl/controller_lifecycle.go#L83) — `hostControllerLifecycle`, `createControllerSpace`; controller_deploy.go `startControllerSpaceImpl`, `stopControllerSpace`; space_archive.go export/import wrappers.
6. <a id="source-6"></a> [Input boundary](../../cmd/yoloctl/agent.go#L319) — `promptYesNo`; agentbox_reconcile.go `runEditor`, session_client.go `attachToSession`, controller_mounts.go `controllerSpaceShell`, install_migration.go `confirmInstall`.
7. <a id="source-7"></a> [Installer output](../../cmd/yoloctl/install_output.go#L34) — `paint`, `ok`, `warn`, `bad`; install.go command outcome wrapper.
8. <a id="source-8"></a> [Authority warnings](../../cmd/yoloctl/privileged_operator.go#L65) — `guardPrivilegedLaunch`, `warnBoxAuthority`; controller_operator.go `warnBoxOperator`; CLI regression `TestCLIPrivilegedAuthoritySurvivesMessageCap`.

9. <a id="source-9"></a> [Launcher capture](../../internal/lima/diagnostics.go#L213) — `WithProgress`, `runWithProgress`; lima.go lifecycle callers; diagnostics_test.go progress and daemon-FD fixtures.

10. <a id="source-10"></a> [Ephemeral notice](../../cmd/yoloctl/agentbox.go#L1759) — `noticeEphemeralWorkdir`, `createAgentbox`; ephemeral_notice_test.go two-pass warm/admission regression.

11. <a id="source-11"></a> [Lock-wait progress](../../cmd/yoloctl/space_operation.go#L25) — `acquireSpaceOperationLock`; space_lock_progress_test.go exercises real lock acquisition, cancellation and uncontended silence.

12. <a id="source-12"></a> [Remaining CLI notices](../../cmd/yoloctl/image.go#L115) — `newImageRefreshCmd`; space_lifecycle.go `deleteSpaceRuntime`, session_client.go `attachToSession`, controller_client.go `callControllerTerminal`.
13. <a id="source-13"></a> [Terminal diagnostic boundary](../../cmd/yoloctl/controller_stderr.go#L16) — `attachLogWriter`, `rawTerminalStderr`; owned_notice_test.go raw PTY, wire and caller-writer/stream controls.

14. <a id="source-14"></a> [Child stderr under the block](../../cmd/yoloctl/infra_stderr.go#L1) — `infraStderr`, `infraLines`; controller_deploy.go `nativeInfrastructure`; cli_logging.go `cliOperation`; infra_stderr_test.go `TestInfraStderrGoesThroughTheStepsBlock`, `TestCLIOperationLeavesOneFinalLineUnderTheStepsBlock`.

15. <a id="source-15"></a> [ID echoes](../../internal/container/container.go#L927) — `nerdctlEcho`, `Run` (detached), `Start`, `Stop`, `ImageRemove`, `Remove` (`ImagePull` keeps stdout); stop_id.go, remove_id.go; echo_output_test.go `TestNerdctlIDEchoesAreNotShown`.

16. <a id="source-16"></a> [Report routing](../../cmd/yoloctl/cli_logging.go#L32-L73) — `cliLogLines`, `cliReportTo`, `cliNoticeSink`; chat_export.go `exportChat`, `finishChatExportChannel`, `writeChatExport`; controller_sessions.go and space.go listings; image.go, agentbox_reconcile.go; agentbox.go `warnPostAutoupdateFailure`; cli_report_test.go.

17. <a id="source-17"></a> [Restart diagnostics](../../cmd/yoloctl/controller_restart_output.go#L16) — `restartSessionOutput`, `restartDetailStream`; controller_sessions.go `hostSessionRestartOffer`; controller_restart_output_test.go framed ownership/plain/debug/pinned-VT controls.
