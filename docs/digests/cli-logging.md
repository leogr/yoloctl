# CLI log routing and outcomes

Verification: `b63d673d39ee54202ea058b27e9e6f283e49cee2` and accompanying changes.

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
interrupted image jobs and remote `uncertain`/`command-uncertain` states are
Unknown. The latter keeps its existing query guidance without adding a hold
or acknowledgment instruction. A remote error's own successful rendered-outcome
receipt can suppress a duplicate; an unrelated earlier `LastOutcome` cannot.
Error reasons use the reserved `err` attribute, independently of the progress
message cap. At `--log-level error`, failed/unknown outcomes remain visible
while successful outcomes are hidden. Sink failure changes neither operation status nor exit classification.
[1](#source-1) [3](#source-3) [4](#source-4)

[FACT] Host create/start/stop/recreate/destroy and archive commands report
existing phases as steps and end with an outcome after cleanup. Nested phases
inherit the root's parent; nested lifecycle calls do not emit another command
outcome. VM image preparation, disk allocation, boot and controller deployment
use the existing functions and guards, without new effect permission or retry.
Lima lifecycle launcher stdout/stderr feed the current phase’s detail; explicit stdout
consumers retain their writers. Owned pipes retain bounded private host-log
tails without waiting for descendant-held FDs. [9](#source-9)
[1](#source-1) [5](#source-5)

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

## Sources

1. <a id="source-1"></a> [CLI logging helpers](../../cmd/yoloctl/cli_logging.go#L17) — `cliLog`, `renderLoggedCLIError`, `cliOperation`, `cliFinishOutcome`, `cliRoutedOutcome`, `wrapHostLogCommand`, `suspendCLISteps`.
2. <a id="source-2"></a> [Reconcile output](../../cmd/yoloctl/agentbox_reconcile.go#L209) — `printDryRun`, `printApplyResult`; account.go command results; internal/openshell/helper_pins.go `existingHelperNotice`.
3. <a id="source-3"></a> [Controller routing](../../cmd/yoloctl/controller_client.go#L115) — `routeControllerCommands`, `reportClientDetached`; internal/controller/client.go `ExitError.State`, `OutcomeRendered`.
4. <a id="source-4"></a> [CLI regressions](../../cmd/yoloctl/cli_logging_test.go#L47) — finalization, error identity/status, uncertainty, sink rejection, JSON/raw command routing and warning separation.
5. <a id="source-5"></a> [Host lifecycle](../../cmd/yoloctl/controller_lifecycle.go#L83) — `hostControllerLifecycle`, `createControllerSpace`; controller_deploy.go `startControllerSpaceImpl`, `stopControllerSpace`; space_archive.go export/import wrappers.
6. <a id="source-6"></a> [Input boundary](../../cmd/yoloctl/agent.go#L319) — `promptYesNo`; agentbox_reconcile.go `runEditor`, session_client.go `attachToSession`, controller_mounts.go `controllerSpaceShell`, install_migration.go `confirmInstall`.
7. <a id="source-7"></a> [Installer output](../../cmd/yoloctl/install_output.go#L34) — `paint`, `ok`, `warn`, `bad`; install.go command outcome wrapper.
8. <a id="source-8"></a> [Authority warnings](../../cmd/yoloctl/privileged_operator.go#L65) — `guardPrivilegedLaunch`, `warnBoxAuthority`; controller_operator.go `warnBoxOperator`; CLI regression `TestCLIPrivilegedAuthoritySurvivesMessageCap`.

9. <a id="source-9"></a> [Launcher capture](../../internal/lima/diagnostics.go#L213) — `WithProgress`, `runWithProgress`; lima.go lifecycle callers; diagnostics_test.go progress and daemon-FD fixtures.

10. <a id="source-10"></a> [Ephemeral notice](../../cmd/yoloctl/agentbox.go#L1759) — `noticeEphemeralWorkdir`, `createAgentbox`; ephemeral_notice_test.go two-pass warm/admission regression.

11. <a id="source-11"></a> [Lock-wait progress](../../cmd/yoloctl/space_operation.go#L25) — `acquireSpaceOperationLock`; space_lock_progress_test.go exercises real lock acquisition, cancellation and uncontended silence.
