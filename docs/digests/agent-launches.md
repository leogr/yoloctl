# Tracked agent launches and settlement

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] A tracked start requires the command wrapper's native space-operation
lock to be still held (a context witness) and takes only the box lock, kept
until the holder is bound. Agentless
paths, including chat ensure, take no lock; the admin attach fast path skips
`--new-session`. Identity decisions probe the unit and marker with errors kept:
an undecidable probe refuses before any change. [1](#source-1) [2](#source-2)
[3](#source-3)

[FACT] A launch moves `allocated` → `dispatching` → `launched`, `closed` or
`uncertain`:

- **Allocated.** Never dispatched; reused once with its generation and token.
- **Dispatching.** Written, with request ID and controller generation, before
  `StartHolderUnit`.
- **Token.** A fresh 128-bit `--launch-token` goes into the holder's marker and
  tombstone.
- **Binding.** Only same-token evidence binds or closes a launch; tokenless
  holders are untracked and mint nothing.
- **Uncertain.** A dispatched launch with no unit, marker or exit record refuses
  until `--new-session`. Receipts are never consulted.

Closing ends a launch, not its conversation. [4](#source-4) [5](#source-5)
[6](#source-6) [7](#source-7)

[FACT] Proof that no holder or agent survived is a positively stopped or
missing runtime followed by a successful start or reconcile, or a managed
restart or recreate whose session cleanup positively stopped the holder and
removed its state.
With it, a launched launch and an interrupted resume close and resume next
time; an interrupted first assignment stays uncertain, because whether its
conversation exists is unknown. The transition is written at the proving start
under its locks; no proof is stored. [8](#source-8) [9](#source-9)

[FACT] Every managed reconcile records the end of a launch, under the caller's
space and box locks: `box edit`/operator apply, `set`, `reconcile`/`--rebuild`,
port changes and chat `--recreate`. A restart or recreate records it at its
proved session cleanup, before the container operation, so a later failure
still leaves a closed, resumable launch; a created container records it
afterwards. An unproved cleanup or in-sync reconcile records nothing.
[10](#source-10) [11](#source-11) [12](#source-12)

## Sources

1. <a id="source-1"></a> [space_operation.go](../../cmd/yoloctl/space_operation.go#L26) — `spaceOperationHeld`, `attachFastPathEligible`; witness at L234.
2. <a id="source-2"></a> [session_identity.go](../../cmd/yoloctl/session_identity.go#L54) — `sessionTrackingFor`, `beginTrackedSession`.
3. <a id="source-3"></a> [session_client.go](../../cmd/yoloctl/session_client.go#L326-L520) — `ensureAgentSession`.
4. <a id="source-4"></a> [launch.go](../../internal/agentsession/launch.go#L116) — `Allocate`, `Dispatch`, `Bind`, `CloseExit`, `SettleResume`, `Close`, `SandboxReplaced`, `Uncertain`.
5. <a id="source-5"></a> [session_identity.go](../../cmd/yoloctl/session_identity.go#L284) — `launch`, `settle`.
6. <a id="source-6"></a> [tombstone.go](../../internal/session/tombstone.go#L13-L60) — `LaunchToken`, `ValidLaunchToken`; holder writes at holder.go L220, L364.
7. <a id="source-7"></a> [unit.go](../../internal/session/unit.go#L33-L150) — `UnitSpec.LaunchToken`, `--launch-token`, `HolderUnitState`, `ProbeVMMarker`; parsed in session_hold.go L47.
8. <a id="source-8"></a> [session_identity.go](../../cmd/yoloctl/session_identity.go#L384) — `applySessionStart`, `recordSessionEnded`.
9. <a id="source-9"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L725) — `startAgentbox`; `recreateAgentbox` at L2572; `removeAgentboxConfig` at L2467.
10. <a id="source-10"></a> [agentbox_reconcile.go](../../cmd/yoloctl/agentbox_reconcile.go#L771) — `reconcileManaged`, its callers and `onSessionEnded` wiring; operator apply in `runControllerDocument`, controller_documents.go L97.
11. <a id="source-11"></a> [replacement_settlement_test.go](../../cmd/yoloctl/replacement_settlement_test.go#L124-L331) — seam regressions, the policy restart, failures after a proved cleanup, an unproved cleanup, missing-member creation and negatives; `recreateChatMember` in chat_runtime.go L417-L440.
12. <a id="source-12"></a> [managed_applier.go](../../cmd/yoloctl/managed_applier.go#L127-L169) — `endSession`, `cleanupManagedSession`, the restart and recreate close details.
