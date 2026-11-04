# Owned attach setup and client loss

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `box.attach` (box attach/use, root attach, managed, passthrough and
guarded chat attachments) is the only operation registered as `EffectGated`
plus `OwnedSetup`. [1](#source-1) [2](#source-2)

[FACT] Each admitted request has a connection context and a separate
execution context derived from the controller's lifetime; the worker, its
process group signals, view pair and private checkpoints follow only the
latter. A per-request mutex orders the effect gate, handoff and detachment.
The gate refuses a departed client, takes ownership, then durably saves
`effect_phase: started` before replying permit. A detach before ownership
cancels the worker with nothing changed (`complete`, nonzero); after it, the
client's transport is only detached. A failed save denies effects. Other
operations, and any phase without ownership, still cancel with their client.
[3](#source-3) [4](#source-4)

[FACT] Worker stdout, stderr, PTY output and view output reach the client
through a sink that never returns an error: a failed client write detaches the
client, and later output goes to a 256 KiB in-memory ring and is dropped, so the
worker neither blocks nor sees EPIPE/SIGPIPE. Client stdin and view input stay
in the existing credited queues until handoff; a detached client's queued
bytes are discarded, so a queued Ctrl-C byte never reaches the setup PTY.
Before handoff a client signal frame is never delivered: it marks the client
detached (a later gate refuses effects), answers a `detached` frame and ends
the connection, so a waiting setup is cancelled and an owned one continues.
After handoff signals reach the worker. Resizes always apply; malformed frames
detach the client.
[3](#source-3) [4](#source-4)

[FACT] After the attachment starts, one signal is forwarded to it and ends it
(the ordinary detach, status 0); a second ends the client unconditionally. The
host client sends its first SIGINT, SIGTERM or SIGHUP as a frame and cancels
on a second; the frame reaches the worker's process group, whose remote attach
detaches and exits. Verified live on 2026-10-28 with
`ATTACH_INTERRUPT_CASES=after-handoff` on a pre-release build: each signal, without a
TTY and under a PTY, ended the client in about 115 ms with status 0 while the
session kept running. [11](#source-11) [12](#source-12) [13](#source-13)

[FACT] The setup checkpoint is acknowledged. The controller saves
`setup_complete`, releases admission, then replies attached or detached. The
worker streams only after an attached reply; `ErrClientDetached` ends it without
streaming while the holder keeps running. A worker that exits without
reading the reply resets the socket; after its completion checkpoint that
counts as EOF, without it as interruption. A `detached` frame makes the client
return `ClientDetached`; the CLI (attach and box use) prints the request ID
and, when setup continues, the `box session` and reattach commands, and exits
75, never an uncertain outcome. The wrapper calls the gate after its
locks and operator/chat revalidation and before any attach write or role
synchronization, including on the admin live-holder fast path; a failed role
synchronization never sends the checkpoint. [5](#source-5) [6](#source-6) [7](#source-7)

[FACT] Finalization uses the execution outcome: only execution cancellation
(authority loss, controller shutdown), worker interruption or explicit effect
uncertainty runs the interrupted classifier. Receipts add informational
`client_detached` and `detach_signal`. Authority loss, worker death after
authorization, lost permission, receipt failure and restart recovery stay
conservative (`uncertain`); drain counts an owned setup as active and its
expiry never kills it. A detached setup runs until its own timeouts or an
explicit stop. [3](#source-3) [8](#source-8)

[FACT] Real worker and client-process regressions kill the client (SIGTERM,
SIGKILL), close its stdout consumer (client SIGPIPE), send cancel, signal,
malformed and overflowing frames, race detach with permission, keep typeahead
order and cover genuine failures, for admin and operator principals; the PTY
Ctrl-C case needs `/dev/ptmx`. CLI-path processes (the real signal context and
both wrappers) prove that one SIGTERM, SIGINT or SIGHUP during owned setup exits
75 while no signal reaches the setup. They do not establish live VM behavior;
the standalone `smoke-attach-interrupt` stage is the live gate.
[9](#source-9) [10](#source-10) [11](#source-11) [12](#source-12)

## Sources

1. <a id="source-1"></a> [cmd/yoloctl/controller_internal.go](../../cmd/yoloctl/controller_internal.go#L69) — `controllerOperations["box.attach"]`.
2. <a id="source-2"></a> [internal/controller/protocol.go](../../internal/controller/protocol.go#L164) — `Operation.OwnedSetup`; the `detached` frame (`Frame.Owned`).
3. <a id="source-3"></a> [internal/controller/server.go](../../internal/controller/server.go#L470) — `ServeConn` execution context, input holding, signals, `setupDone`, gate and finalization.
4. <a id="source-4"></a> [internal/controller/ownership.go](../../internal/controller/ownership.go#L1) — `setupOwner`, `clientOutput`, `clientSink`, `ErrClientDetached`, `ClientDetached`.
5. <a id="source-5"></a> [internal/controller/effects.go](../../internal/controller/effects.go#L90) — acknowledged `WorkerCheckpoints.Release`, `serveWorkerCheckpoints`.
6. <a id="source-6"></a> [cmd/yoloctl/space_operation.go](../../cmd/yoloctl/space_operation.go#L29) — `releaseSpaceOperation`, `beginAttachEffects`, wrapper gate order.
7. <a id="source-7"></a> [cmd/yoloctl/attach_effects_test.go](../../cmd/yoloctl/attach_effects_test.go#L1) — registry, gate order, denial, role-sync failure and detached acknowledgment.
8. <a id="source-8"></a> [internal/controller/receipts.go](../../internal/controller/receipts.go#L22) — receipt fields, `interrupt`, `Recover`.
9. <a id="source-9"></a> [internal/controller/ownership_test.go](../../internal/controller/ownership_test.go#L1) — owned-setup client-loss, race, stream and negative-recovery regressions.
10. <a id="source-10"></a> [internal/controller/worker_test.go](../../internal/controller/worker_test.go#L27) — `TestMain` worker/client helpers.
11. <a id="source-11"></a> [scripts/smoke/attach-interrupt.sh](../../scripts/smoke/attach-interrupt.sh#L1) — live kill and closed-pipe gate with receipt-traced phases; opt-in after-handoff stage.
12. <a id="source-12"></a> [cmd/yoloctl/attach_signal_test.go](../../cmd/yoloctl/attach_signal_test.go#L1) — CLI-path signal regressions before and after handoff; `reportClientDetached`, `exitDetached` in `controller_client.go`/`main.go`.
13. <a id="source-13"></a> [cmd/yoloctl/signals.go](../../cmd/yoloctl/signals.go#L20) — `commandSignalContext`: first signal forwarded, second cancels.
