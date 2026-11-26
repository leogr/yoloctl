# Controller interrupted-outcome recovery

Verification: `a4322a8c233da5f9eeb51239db68fab34d55f9b0` plus accompanying N1/N2 changes; acknowledgment removal re-read at `433a380405cd3f5dc4b1b12b2ed06a6c4ed4a96e` and accompanying changes;
re-read cited symbols when changing behavior.

[FACT] Registered mutations use typed effect gates and recorded footprints.
After permission, checkpoints carry scope expansions, HMAC-SHA256 document
postimage proofs and one known effects-complete result. Fingerprints bind the
native disk, receipt ID, verifier/version, document identity, owner and mode;
the root-private recovery key never leaves the controller. A malformed key is
refused, never regenerated. Completion is recorded before releasing native apply
locks, including role publication; typed no-completion errors remain internal
signals rather than permission to retry. [1](#source-1) [2](#source-2)
[3](#source-3) [4](#source-4) [6](#source-6) [7](#source-7)

[FACT] An ended execution without a proven result is `interrupted`, including an
authorized exec whose command may have run. Setup-complete, a known ungranted
gate and a recorded completion retain their proven dispositions. Legacy
`uncertain` and `command-uncertain` records are migrated at startup;
`acknowledged` stays readable. Nothing is replayed. Ordinary interrupted records are final
and evictable. Qualified private image ownership stays in strict orphan facts;
earlier-format unfinished jobs retain a strict residual in their receipt. Their
first verified boot is recorded without trusting the old incarnation. A later
verified boot releases the lease and marks the exact alias pending the native
storage cleanup inventory; the residual clears only after observed alias absence.
Failures preserve remaining protection, including after a partial lease deletion.
Ended strict residual facts are excluded from admission-capacity accounting; they
are never evicted to admit work. Image cleanup and abandoned
jobs also close interrupted, not as success. [5](#source-5) [8](#source-8) [20](#source-20)

[FACT] Finished outcomes hold nothing. Running permitted executions still reserve
their footprints and conflicting gates wait; completed setups and authorized
exec commands do not reserve controller-managed state. Public receipt views omit
private proofs. No option or method acknowledges an outcome; none changes
records or claims success. An interrupted result is a failed command with
its method and current observation or state-read advice. [9](#source-9)
[10](#source-10) [15](#source-15)

[FACT] The existing controller checker performs one bounded closing observation
per interrupted record. Only a trustworthy scope for the current disk selects
boxes; legacy, unknown and foreign-disk scope records not observed with a reason,
never the state of a same-named current box. The fixed-argv verifier uses private
inherited descriptors, native locking and read-only storage identity verification.
It reports full engine container ID and running/stopped/absent, separately from
historical command success. Failed/unavailable/timed-out observations are retained
as not observed; they do not keep an operation open. A publication failure keeps
the completed check in memory for publication retry, without repeating observation
or replacing a newer completion or strict image fact. [11](#source-11)
[12](#source-12) [13](#source-13) [18](#source-18)

[FACT] The same bounded check can verify the existing account declaration and
chat-revoke document proofs. Only exact fingerprints and an unchanged receipt
publish `recovered`/`applied`. A mismatch, unavailable verifier or timeout leaves
the record interrupted, remembers that check and is not polled again once its check is durably recorded,
including after controller restart. The check describes observed state, never completes or
replays the original command. Each receipt check is bounded to5s, a pass to30s;
drain cancels and waits. Storage repair retains its existing backoff.
[11](#source-11) [12](#source-12) [18](#source-18)

[FACT] Ordinary records use the live controller admission, not durable bytes, as
permission and reservation authority. Recording failures keep the latest value in
memory for ordered late publication without replacing the command's result.
Actual UUID/owner/live-bind safety remains separate; workers and verifier never
prepare that bind. Image working facts remain strict. See
[receipt recording](controller-recording.md). [11](#source-11) [14](#source-14)
[17](#source-17)

[FACT] Source tests cover classification, legacy migration, the absent acknowledgment methods,
bounded scope/disk selection and a single persisted observation; they do not
establish native VM/power-loss qualification. [16](#source-16) [19](#source-19)

## Sources

1. <a id="source-1"></a> [internal/controller/effects.go](../../internal/controller/effects.go#L1) — checkpoint kinds, `BeginScopedEffects`, `ExpandEffects`, `RecordDocument`, `CompleteEffects`, `serveWorkerCheckpoints`.
2. <a id="source-2"></a> [cmd/yoloctl/controller_internal.go](../../cmd/yoloctl/controller_internal.go#L40) — `controllerOperations` gates and footprints.
3. <a id="source-3"></a> [cmd/yoloctl/operation_scope.go](../../cmd/yoloctl/operation_scope.go#L130) — footprint resolution, bounded actual/desired account dependencies and `wrapperGatesEffects`.
4. <a id="source-4"></a> [internal/controller/recovery.go](../../internal/controller/recovery.go#L1) — evidence types, validation, `LoadRecoveryKey`, fingerprints.
5. <a id="source-5"></a> [internal/controller/receipts.go](../../internal/controller/receipts.go#L1) — `Receipt` fields, `interrupt`, `terminal`, `begin`.
6. <a id="source-6"></a> [cmd/yoloctl/space_operation.go](../../cmd/yoloctl/space_operation.go#L1) — wrapper gate and completion placement.
7. <a id="source-7"></a> [cmd/yoloctl/operation_completion_test.go](../../cmd/yoloctl/operation_completion_test.go#L1) — wrapper completion predicate; dispatcher ordering in document_completion_test.go, chat_completion_owner_test.go and `completeBeforeUnlock`.
8. <a id="source-8"></a> [internal/controller/recovery_test.go](../../internal/controller/recovery_test.go#L1) — real-worker evidence, validation, eviction and key custody.
9. <a id="source-9"></a> [internal/controller/holds.go](../../internal/controller/holds.go#L1) — running reservations, interrupted rendering and legacy `acknowledged` records.
10. <a id="source-10"></a> [internal/controller/holds_test.go](../../internal/controller/holds_test.go#L1) — running reservation and public inspection regressions.
11. <a id="source-11"></a> [internal/controller/recovery_scheduler.go](../../internal/controller/recovery_scheduler.go#L1) — ledger, `RunRecovery`, `repairStorage`, `publishApplied`.
12. <a id="source-12"></a> [internal/controller/verifier.go](../../internal/controller/verifier.go#L1) — `RunVerifier` descriptors and bounds.
13. <a id="source-13"></a> [cmd/yoloctl/controller_verify.go](../../cmd/yoloctl/controller_verify.go#L25) — read-only verifier worker.
14. <a id="source-14"></a> [cmd/yoloctl/controller_internal.go](../../cmd/yoloctl/controller_internal.go#L337-L386) — `serveController` current identity verification;
    [degraded recovery](../../cmd/yoloctl/controller_internal.go#L462-L464).
15. <a id="source-15"></a> [internal/controller/server.go](../../internal/controller/server.go#L1) — admission, gate, ping, receipt views and finalization.
16. <a id="source-16"></a> [internal/controller/recovery_scheduler_test.go](../../internal/controller/recovery_scheduler_test.go#L1) — storage identity loss/restoration, responsive reads, repair and drain regressions.

17. <a id="source-17"></a> [runtime.go](../../internal/controller/runtime.go#L19) — read-only loading/verification; explicit lifecycle preparation.

18. <a id="source-18"></a> [internal/controller/observation.go](../../internal/controller/observation.go#L1) — scope/disk selection, interruption text and one-shot observations.
19. <a id="source-19"></a> [internal/controller/interrupted_test.go](../../internal/controller/interrupted_test.go#L1) — migration, observation and compatibility controls.

20. <a id="source-20"></a> [internal/controller/image_backend.go](../../internal/controller/image_backend.go#L1) — strict earlier-format boot facts, alias candidates and post-GC reconciliation; controls in legacy_image_residual_test.go.
