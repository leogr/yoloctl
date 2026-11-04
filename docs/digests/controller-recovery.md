# Controller interrupted-outcome recovery

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Receipts carry the native disk identity and proof version 1 or 2;
version 2 adds named image resources. Every registered mutation is effect-gated with a footprint
policy. The gate durably records the worker's typed footprint (box/account
names, the shared chat, image and port domains and named image references) and verifier kind in the
update that grants permission. Private worker footprints resolve under the native lock, including default
chat servers, immutable members and actual accounts/forwards. Unknown
dependencies hold the space. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] After permission the descriptor accepts durable scope expansions (image
domain at the build guard, chat at role publication), document proofs and one
effects-complete checkpoint with the known result status. Each is
acknowledged only after its receipt flush. A proof stores an HMAC-SHA256
fingerprint of the exact postimage. The fingerprint is bound to disk, receipt
ID, verifier version, document identity and owner/mode. The key lives at
root-private `.controller/recovery/key` and never leaves the controller. A
malformed key is refused, never regenerated. [1](#source-1) [4](#source-4) [5](#source-5)

[FACT] The space-operation wrapper records completion after a successful role
sync and before releasing its locks. Each dispatcher that takes the native
lock itself (documents, auth import, chat state/runtime/channels/participation)
records it from its own returned outcome before its unlock, after any role
publication. Chat runtime and channels report failed role, membership or
channel publication after a save through the same input. All use one
predicate: returned normally (no panic), a live context, not
`EffectsUncertain`, and no failed publication. Mutations without their
own lock record it when the handler returns. A normal error is recorded as a
failed result. [6](#source-6) [7](#source-7)

[FACT] Interruption classification: a waiting gate becomes `recovered`
`not-applied`, and a recorded completion becomes `recovered`
`recorded-completion` with its status. Setup-complete and authorized exec keep
their meanings; anything else stays `uncertain`. A worker that reports
uncertainty after completing loses its checkpoint, even when the controller
then terminates it with a signal. Strict decoding rejects future versions,
legacy records carrying evidence or `recovered`, mismatched dispositions and
the reserved `consistent-result-unknown`. Recovered receipts are evictable;
unresolved ones are not. Qualified abandoned private-image receipts become
evictable only after their independent durable orphan fact exists; they retain
an unknown execution result without an admission hold. Private image preflight
has no output reservation; deleting cleanup has a separate images-domain permit
and retains uncertainty on failure. Ended image metadata owners settle the
receipt-failure ledger without proving service-side work stopped.
See [image preparation](image-preparation.md). [5](#source-5) [8](#source-8)

[FACT] Holds are derived from durable receipts at admission and at each gate.
An unresolved receipt holds its recorded writes. An unscoped, legacy or
other-disk receipt holds the space; only such outcome holds refuse admission.
A gate is refused when its writes or reads intersect a held write, so a hold
created while the worker waited for its lock still applies. An effect-started
execution reserves its writes until its disposition is durable, and a gate
meeting only reservations waits for them. Completed setups and authorized
execs never reserve. Operator refusals name only requested resources. Public
receipt views omit private document proofs, retaining disposition and footprint.
[9](#source-9) [10](#source-10)

[FACT] One controller checker runs at startup, after each new unresolved
outcome and after a storage failure. Failed passes are retried at 1, 2, 4, 8,
15 and 30 s; each receipt check is bounded to 5 s and each pass to 30 s,
resuming at the next receipt. Drain cancels and waits for it. Verifiers are a
reviewed allowlist: account declaration writes and the desired
`chat.state.revoke` room document. A fixed-argv `controller-internal verify`
worker reads the named documents under the space and entity locks and
rechecks UUID/marker/live bind without preparation, then returns bytes and
metadata. The controller publishes `recovered` `applied`
only if every proof matches and the receipt is unchanged. Mismatches are not
retried. [11](#source-11) [12](#source-12) [13](#source-13) [17](#source-17)

[FACT] A failed receipt flush sets the storage hold and a ledger entry with
the controller's intended state; flushes made while the hold stands are also
ledgered. A pass reopens admission only after every execution with a ledger
entry has settled. Outside the server mutex, a bounded read-only check
revalidates UUID, marker and live bind; publication rechecks ledger/drain.
A file probe and complete strict inventory are also required. A never-run admission is classified not-applied.
A failed acknowledgment is read back and never repeated. At startup,
receipt-directory, receipt-recovery, recovery-key and interrupted-inventory
failures start a read-only server that retries them; invalid disk, store or
activation still refuses. A read-only controller answers ping with a stderr
notice. [11](#source-11) [14](#source-14) [15](#source-15)

[FACT] Tests cover recovery, not live VM/power-loss behavior. [16](#source-16)

## Sources

1. <a id="source-1"></a> [internal/controller/effects.go](../../internal/controller/effects.go#L1) — checkpoint kinds, `BeginScopedEffects`, `ExpandEffects`, `RecordDocument`, `CompleteEffects`, `serveWorkerCheckpoints`.
2. <a id="source-2"></a> [cmd/yoloctl/controller_internal.go](../../cmd/yoloctl/controller_internal.go#L35) — `controllerOperations` gates and footprints.
3. <a id="source-3"></a> [cmd/yoloctl/operation_scope.go](../../cmd/yoloctl/operation_scope.go#L130) — footprint resolution, bounded actual/desired account dependencies and `wrapperGatesEffects`.
4. <a id="source-4"></a> [internal/controller/recovery.go](../../internal/controller/recovery.go#L1) — evidence types, validation, `LoadRecoveryKey`, fingerprints.
5. <a id="source-5"></a> [internal/controller/receipts.go](../../internal/controller/receipts.go#L1) — `Receipt` fields, `interrupt`, `terminal`, `begin`.
6. <a id="source-6"></a> [cmd/yoloctl/space_operation.go](../../cmd/yoloctl/space_operation.go#L1) — wrapper gate and completion placement.
7. <a id="source-7"></a> [cmd/yoloctl/operation_completion_test.go](../../cmd/yoloctl/operation_completion_test.go#L1) — wrapper completion predicate; dispatcher ordering in document_completion_test.go, chat_completion_owner_test.go and `completeBeforeUnlock`.
8. <a id="source-8"></a> [internal/controller/recovery_test.go](../../internal/controller/recovery_test.go#L1) — real-worker evidence, validation, eviction and key custody.
9. <a id="source-9"></a> [internal/controller/holds.go](../../internal/controller/holds.go#L1) — derived holds, reservations, refusals.
10. <a id="source-10"></a> [internal/controller/holds_test.go](../../internal/controller/holds_test.go#L1) — holds, reservations, public inspection/redaction and acknowledgment regressions.
11. <a id="source-11"></a> [internal/controller/recovery_scheduler.go](../../internal/controller/recovery_scheduler.go#L1) — ledger, `RunRecovery`, `repairStorage`, `publishApplied`.
12. <a id="source-12"></a> [internal/controller/verifier.go](../../internal/controller/verifier.go#L1) — `RunVerifier` descriptors and bounds.
13. <a id="source-13"></a> [cmd/yoloctl/controller_verify.go](../../cmd/yoloctl/controller_verify.go#L25) — read-only verifier worker.
14. <a id="source-14"></a> [cmd/yoloctl/controller_internal.go](../../cmd/yoloctl/controller_internal.go#L327) — `serveController` current identity verification and degraded recovery.
15. <a id="source-15"></a> [internal/controller/server.go](../../internal/controller/server.go#L1) — admission, gate, ping, acknowledgment and finalization.
16. <a id="source-16"></a> [internal/controller/recovery_scheduler_test.go](../../internal/controller/recovery_scheduler_test.go#L1) — storage identity loss/restoration, responsive reads, repair and drain regressions.

17. <a id="source-17"></a> [runtime.go](../../internal/controller/runtime.go#L19) — read-only loading/verification; explicit lifecycle preparation.
