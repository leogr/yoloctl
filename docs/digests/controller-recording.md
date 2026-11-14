# Controller receipt recording

Verification: `a4322a8c233da5f9eeb51239db68fab34d55f9b0` plus accompanying N2 changes;
re-read cited symbols when changing behavior.

[FACT] An ordinary admission registers one original live receipt pointer, request
ID, method, principal, start time, generation and disk. Begin, gate, setup,
expansion, evidence, completion and finalization retain the current value even
when publication fails. The caller receives the command's result. Running
footprint exclusion reads this live table, never the receipt inventory. Successful
recording does not settle a worker. Ordinary completed-history retention runs
after publication from a complete inventory; live and strict residual owners
are never evicted to admit work. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Publication is serialized under the controller mutex with increasing
revisions. Repair flushes current live and ended dirty records without waiting
for workers, marking them recorded late. A failed rename/directory-sync outcome
stays dirty. Recovery skips the full admission identity held by this process;
it cannot close live work or an ended result waiting for publication. After a
process ends, an old waiting file proves no ungranted gate: recovery closes it
interrupted. Nothing replays a command. [1](#source-1) [4](#source-4)

[FACT] An unreadable per-ID file is never treated as absence. The operation runs
in memory, but publication waits for a conclusive absent/same-admission read.
If another admission owns the path, its record is never replaced. The live
controller keeps a collision note; the later result is in its log and is labelled
not stored. Human `space operation` inspection displays method, principal and
start time; after controller restart it cannot identify a later same-ID command
that left no record. Visibility is applied independently to both admissions.
No client automatically queries or retries its own request. Admission and end
logs identify request, method and result. [1](#source-1) [5](#source-5)

[FACT] Native incarnation and optional bootstrap handoff checks require the
original registered pointer, waiting/started phase, principal, generation, disk,
scope, context and original owner lifetime. Unavailable/stale durable copies are
diagnostic only. Bootstrap does not grant effects. Operator publication and audit
maintenance inspect live controller state; the image preflight child's inherited
ready/permit pipes remain its authority. Actual native UUID, ownership and bind
verification remains separate from record health. Invalid recovery keys still
refuse startup; no repair manufactures native authority. [2](#source-2)
[6](#source-6) [7](#source-7) [11](#source-11) [12](#source-12) [13](#source-13) [14](#source-14)

[FACT] Image job stages, leases and orphan facts remain strict working state:
a failed write ends that job step with its own error, never a late-only fact.
Ordinary operations using existing images do not inherit a generic recording
fence. Background maintenance remains skipped while recording is unhealthy.
[8](#source-8) [9](#source-9)

[FACT] Focused source tests exercise failed writes, real checkpoint dispatch,
late flush while a worker is active, stale history/collision visibility and
independent native-identity checks. They do not qualify native VM or power-loss
behavior. [10](#source-10)

## Sources

1. <a id="source-1"></a> [live_receipts.go](../../internal/controller/live_receipts.go#L1) — admission, publication, collision, strict ownership and native identity.
2. <a id="source-2"></a> [server.go](../../internal/controller/server.go#L1) — dispatch, gate, evidence, setup and finalization.
3. <a id="source-3"></a> [holds.go](../../internal/controller/holds.go#L1) — live reservation reads.
4. <a id="source-4"></a> [recovery_scheduler.go](../../internal/controller/recovery_scheduler.go#L1) — repair, identity checks and recovery exclusion.
5. <a id="source-5"></a> [operation_views.go](../../internal/controller/operation_views.go#L1) — visible memory/history/collision views.
6. <a id="source-6"></a> [native_nonce.go](../../internal/controller/native_nonce.go#L1) — original live owner and handoff checks.
7. <a id="source-7"></a> [controller_internal.go](../../cmd/yoloctl/controller_internal.go#L1) — startup key, inherited image permit, operator and audit routes.
8. <a id="source-8"></a> [image_jobs.go](../../internal/controller/image_jobs.go#L1) — strict job state and ordinary cleanup receipts.
9. <a id="source-9"></a> [maintenance.go](../../internal/controller/maintenance.go#L1) — idle maintenance admission.
10. <a id="source-10"></a> [live_receipts_test.go](../../internal/controller/live_receipts_test.go#L1) — failure and ownership controls.
11. <a id="source-11"></a> [worker_handoff_owner_linux.go](../../internal/controller/worker_handoff_owner_linux.go#L1) — original waiting bootstrap admission, Prepare and publishStarted.
12. <a id="source-12"></a> [controller_images.go](../../cmd/yoloctl/controller_images.go#L1) — image preflight inherited permit.
13. <a id="source-13"></a> [controller_audit_linux.go](../../cmd/yoloctl/controller_audit_linux.go#L1) — in-process audit authority.
14. <a id="source-14"></a> [operator_retire.go](../../internal/controller/operator_retire.go#L1) — operator publication checks.
