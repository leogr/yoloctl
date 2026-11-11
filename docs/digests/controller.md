# VM controller, transport and recovery

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Public clients reuse the service's Cobra definitions and route registered
operations to one root controller inside the selected VM. Host clients use a
fixed adapter SSH bridge to its private admin socket; operator clients use their
provisioned Unix endpoint. Workers execute the same services locally, scoped by
a fixed root-owned context. There is no host controller daemon or generic
shell/filesystem RPC. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The host stores schema-3 boot authority; defaults, box/account declarations
and secrets live in private `config/` directories on the existing ext4 data
disk. Runtime loading verifies the selected executable, disk UUID/marker/live
bind and directory ownership without preparing or repairing storage. Explicit
deployment/import owns bind preparation. Native declaration I/O is bounded, no-follow and
restricted to that store. Read-only storage verification bounds output capture and
pipe settlement; incomplete output supplies no identity. [31](#source-31)
Writes flush the file, rename, then flush its parent;
post-replacement flush failure reports an uncertain commit. Native deletion
also flushes its directory. Host shares carry no declaration lock or write.
[3](#source-3) [4](#source-4) [5](#source-5) [6](#source-6)

[FACT] Protocol 3 uses bounded length-prefixed JSON frames. Handshake requires
matching bundle, protocol, space and supplied generation. Methods and flags are
validated against the explicit registry/shared Cobra surface; positional flag
injection is rejected. Secret/import payloads use a private worker descriptor,
not argv or receipts. Fixed worker intake claims payload/checkpoint ownership
and any recognized phase/launch handles before runtime loading can exec a child;
its typed envelope must use exact canonical encoding. Optional launch observation
does not grant effects. [28](#source-28) [29](#source-29) [30](#source-30) Stdout/stderr remain separate for nonterminal calls;
terminal calls relay a PTY, resize and signals. Managed attachments additionally
relay a private duplex session channel, with independent input credits and
no snapshot parsing from stdout. [21](#source-21) Stdin EOF does not stop output;
input credits bound buffering. The inherited checkpoint stream has one physical
owner for effect, image, advisory and original-handoff checks; canceled waiters
leave active exchanges untouched. Fully received parent uncertainty survives
concurrent cancellation. [32](#source-32) Untouched local cancellation also
leaves the original handoff reusable; active stream loss and original pin loss
remain sticky. Classification ignores nested caller-supplied error markers.
[33](#source-33) The SDK process-view borrow retains that same
handoff and full post-setup native context. One OS-thread lock spans independent
namespace comparison, real process observations, final checks and observer Close;
original handoff/channel Close remains with their existing owners. [34](#source-34)
Control-only requests send no stdin/EOF frames.
A worker's structured log records use one more private pipe, appended after
every other inherited descriptor and recorded by pipe identity; intake claims
it close-on-exec before runtime loading, so tools never inherit it. The
request carries the host verbosity. The controller relays each record as a
`log` frame and reads the pipe to EOF before the exit frame, waiting at most
2 s after the worker exits, then sends an explicit incomplete-stream record;
a partial or corrupt tail is reported, not parsed. An exit frame names an
`uncertain` or `command-uncertain` receipt state; the client exposes it, and
whether it already rendered a failed/unknown outcome record, on `ExitError`.
[35](#source-35) [36](#source-36)
Bridge closure permits a bounded graceful exit before killing a stuck process.
[17](#source-17) [18](#source-18) [2](#source-2) [7](#source-7) [8](#source-8)

[FACT] One native space lock serializes declaration/runtime apply and update.
Controller-owned image preparation runs outside it; see
[image preparation](image-preparation.md) for cap, private publication and cancellation.
Entity locks follow it. Catalog/status reads bypass it; identity-guarded
chat attachment lookup still takes a shared lock. Existing-holder attachment and
active exec/attach streams release setup admission; local editor waits hold no
mutation lock. Editor save compares declaration/settings bytes and file identity,
catalog digest and boot generation, rejecting concurrent or identical-inode
replacement. Each worker uses a private catalog snapshot from two matching
inventories of the read-only host catalog directories. [9](#source-9)
[10](#source-10) [11](#source-11)

[FACT] A drain persists maintenance before waiting for admitted setup and a
running recovery check; new work is refused. Host-only resume durably clears
maintenance after export without restarting workers or clearing uncertain
outcomes or receipt failures. Independent session units survive controller
restart. The guest supervisor terminates old controller workers before a new
instance starts; startup then recovers pending receipts with the finalization
classifier. No request is automatically replayed after a lost reply.
Acknowledgment means reviewed, never successful, retried or undone; `pending`
is never acknowledged. `--acknowledge-all` reviews every finished unknown
outcome of one mutex-held inventory, reports any maintenance still in effect
and stops at a failed save with the saved prefix under the storage hold.
Gating, evidence, automatic recovery, footprint holds and receipt-storage
repair are in [interrupted-outcome recovery](controller-recovery.md).
[12](#source-12) [13](#source-13) [22](#source-22) [26](#source-26)

[FACT] Effect gates start at `effect_phase: waiting`. Private duplex FD 4
acknowledges `BeginEffects` only after durably saving `started`; stdout cannot
authorize effects. After authorization, signals, panic or a missing normal
completion, process-wait errors, malformed checkpoints and explicit uncertain
effects retain uncertainty unless setup or completion was durably recorded.
Only `box.attach` also owns authorized setup: its client's loss detaches
instead of canceling, and finalization follows the execution, not the client.
[23](#source-23) [24](#source-24) [8](#source-8) [12](#source-12) [27](#source-27)

[FACT] Gated `box.exec` permits its command before setup release. Later
interruption or a missing remote exit status records non-fencing
`command-uncertain`: queryable, never replayed, optionally acknowledged, and
never reserving its box. Known remote exits keep their status; legacy exec
records stay `uncertain`. [2](#source-2) [13](#source-13) [25](#source-25)

[FACT] Unit/PTY tests exercise frame/scope rejection, byte streams, flow control,
resize/final output, signal status, cancellation, revocation, failed receipt
writes, drain, recovery, publication and
catalog snapshots. They do not establish Linux/macOS live VM behavior; the
isolated controller and archive scripts are separate runtime gates.
[14](#source-14) [15](#source-15) [16](#source-16)

[FACT] Frontend fixtures inject operator metadata detection so host-offline
tests remain isolated inside an operator-enabled development box. Operator
guard tests exercise disallowed commands and cross-space routing without a
runtime connection. Production detection still reads the provisioned endpoint;
this test seam does not change server authorization. [19](#source-19)
[20](#source-20)

## Sources

1. <a id="source-1"></a> [cmd/yoloctl/controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L30) — newClientRootCmd.
2. <a id="source-2"></a> [cmd/yoloctl/controller_internal.go](../../cmd/yoloctl/controller_internal.go#L35) — controllerOperations, runControllerWorker, controllerCommandArgs.
3. <a id="source-3"></a> [internal/controller/runtime.go](../../internal/controller/runtime.go#L18) — `LoadRuntime`, `VerifyRuntimeStorage`, `PrepareRuntimeForLifecycle`, `PrivateDir`.
4. <a id="source-4"></a> [internal/spaceconfig/bootstrap.go](../../internal/spaceconfig/bootstrap.go#L24) — Bootstrap, Settings.
5. <a id="source-5"></a> [internal/manifestio/io.go](../../internal/manifestio/io.go#L26) — Read, Write, Remove.
6. <a id="source-6"></a> [internal/safepath/safepath.go](../../internal/safepath/safepath.go#L159) — WriteFile, ReadFileInfo, RemoveFile.
7. <a id="source-7"></a> [internal/controller/protocol.go](../../internal/controller/protocol.go#L17) — framing, MatchHello, Authorize, Request.Validate.
8. <a id="source-8"></a> [internal/controller/worker.go](../../internal/controller/worker.go#L28) — RunWorker process/PTY lifecycle.
9. <a id="source-9"></a> [cmd/yoloctl/space_operation.go](../../cmd/yoloctl/space_operation.go#L29) — native mutation guard and fast attach.
10. <a id="source-10"></a> [cmd/yoloctl/controller_documents.go](../../cmd/yoloctl/controller_documents.go#L47) — revision checks and editor apply.
11. <a id="source-11"></a> [internal/controller/catalogs.go](../../internal/controller/catalogs.go#L16) — CatalogSnapshot.
12. <a id="source-12"></a> [internal/controller/server.go](../../internal/controller/server.go#L62) — Drain, admission, recovery refusal, `acknowledgeAll`, receipts and stream dispatch.
13. <a id="source-13"></a> [internal/controller/receipts.go](../../internal/controller/receipts.go#L79) — authorizedCommand, interrupt, Recover, Begin, retention.
14. <a id="source-14"></a> [internal/controller/protocol_test.go](../../internal/controller/protocol_test.go#L22) — recovery, framing, stream, drain and publication regressions.
15. <a id="source-15"></a> [internal/controller/worker_test.go](../../internal/controller/worker_test.go#L77) — worker byte streams, PTY and cancellation regressions.
16. <a id="source-16"></a> [scripts/smoke/space-archive.sh](../../scripts/smoke/space-archive.sh#L1) — archive runtime smoke; controller-operator sibling covers live operator RPC.
17. <a id="source-17"></a> [internal/controller/client.go](../../internal/controller/client.go#L47) — Call, methodHasInput.
18. <a id="source-18"></a> [internal/controller/transport.go](../../internal/controller/transport.go#L39) — bounded graceful commandTransport.Close; subprocess regressions in transport_test.go.
19. <a id="source-19"></a> [cmd/yoloctl/controller_client_test.go](../../cmd/yoloctl/controller_client_test.go#L12) — stubOperatorClient and frontend guard/routing regressions; host-offline fixture in controller_test.go.
20. <a id="source-20"></a> [cmd/yoloctl/controller_client.go](../../cmd/yoloctl/controller_client.go#L35) — loadOperatorClient and selectControllerTarget; production metadata loader unchanged.

21. <a id="source-21"></a> [Managed view transport](session-views.md) — request validation, descriptors, protocol framing and transport tests.

22. <a id="source-22"></a> [Guest supervisor](controller-supervision.md) — failure-only restart, verified cgroup cleanup, stop joining and VM-image rollout.
23. <a id="source-23"></a> [Effect protocol](../../internal/controller/effects.go#L34) — trusted gate, checkpoints and completion.
24. <a id="source-24"></a> [Effect regressions](../../internal/controller/effects_test.go#L18) — real workers, locks, malformed/stdout bytes, durable checkpoint failures, recovery, exec outcomes and retention.
25. <a id="source-25"></a> [cmd/yoloctl/run.go](../../cmd/yoloctl/run.go#L95) — `execInAgentboxInteractive`; production-registry regressions in exec_receipt_test.go.
26. <a id="source-26"></a> [Batch acknowledgment](../../internal/controller/acknowledge_test.go#L54) — `acknowledgeAll` regressions; CLI in `newSpaceOperationCmd`.
27. <a id="source-27"></a> [Owned attach setup](attach-setup.md) — gate order, error-free output, held input and acknowledged handoff.

28. <a id="source-28"></a> [Early worker intake](../../cmd/yoloctl/controller_worker_intake.go#L106) — pre-runtime capture; runtime/auth/effect checks still apply.
29. <a id="source-29"></a> [Mandatory ownership](../../internal/controller/worker_private_input_linux.go#L120) — raw full-pair eligibility and unknown handles untouched.
30. <a id="source-30"></a> [Canonical payload](../../internal/controller/worker_private_input.go#L37) — exact closed typed worker decoding.

31. <a id="source-31"></a> [Storage verification transport](../../internal/accountstate/verify.go#L44) — verify-only bounded output/pipe settlement and closed UUID response; other account actions keep their existing path.

32. <a id="source-32"></a> [Physical checkpoint owner](../../internal/workercheckpoint/channel.go#L97) — sole original File, closed operations, cancellation admission and complete-reply preservation; transport possession supplies no VM/SDK grant.

33. <a id="source-33"></a> [Original handoff checkpoint](../../internal/vmanchor/worker_checkpoint_binding_linux.go#L76) — local cancellation versus sticky original source/stream failure; physical channel classification is outermost-only.

34. <a id="source-34"></a> [SDK original source borrow](../../internal/openshell/worker_process_source_linux.go#L1) — concrete original handoff, sticky context selection, phase-bound parent checks before final local process view and owned-only observer cleanup; no source installer.

35. <a id="source-35"></a> [Worker log pipe](../../internal/controller/worker.go#L73) — log pipe creation, last-slot `LogFD`/`LogPipe`, relay goroutine and `joinWorkerLog` before return; `OpenWorkerLog` in worker_log_linux.go; intake claim in cmd/yoloctl/controller_worker_intake.go and `InitWorker` in controller_internal.go `runControllerWorker`.
36. <a id="source-36"></a> [Log frames](../../internal/controller/log_transport.go#L1) — `LogVerbosity`, `frameLog`, `relayWorkerLog`, `joinWorkerLog`; client.go `case "log"`/`"exit"`, `ExitError.Rendered`/`State`, `OutcomeRendered`; server.go exit `State`.
