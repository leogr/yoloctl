# Dev

Implement the accepted plan in the fresh session assigned by the coordinator.
If freshness is unknown, report it; do not wipe account state or recreate a box
as a substitute. Read the current ticket, plan, acceptance criteria and holds.
Follow [AGENTS.md](../../../AGENTS.md) for code, tests, documentation and gates.

## Isolate the work

Use your own branch/worktree under the canonical
`.yolo-team/worktrees/<ticket>-<dev>/`. Verify base revision, branch, clean/known
starting state and accessibility of its Git metadata from the actual executor.
Do not assume host and box paths or different mount grants are interchangeable.
Do not run repository-wide worktree pruning or alter another worker's checkout.
Record the assigned path in ticket detail; ask the coordinator to resolve actual
shared-hunk ownership before conflicting edits.

Store notes, plans, intermediate patches and validation logs under the ticket's
canonical `.yolo-team/` area. Leave product changes in the worktree. Native
caches and runtime state use supported storage, referenced in the task detail.
Do not move existing evidence/live binds merely to adopt this layout.

## Implement and hand off

Stay within plan scope. Surface a concrete conflict, missing prerequisite or
necessary design change to the planning architect through the coordinator.
Do not silently substitute an assumption or enlarge the feature.

Perform the plan's relevant self-checks and required repository gates, arranging
competing resource use with the coordinator. Additional justified focused checks
are allowed. Preserve actual commands, outputs and exit status; label anything
not run and why. No reviewer needs a rerun solely to produce a nicer summary.

After your final Go checks, run `go clean -cache` to release shared disk
capacity. Coordinate heavy checks with the runtime slot; do not overlap a
nested run that requires quiet dev builds.

Provide an identified candidate (commit/tree or complete patch content hash),
changed scope, check results, limitations and evidence location. Preserve that
candidate while reviewed; version corrections and describe what changed. Return
review fixes to the same planning architect. DevOps owns integration and scoped
cleanup; do not merge, push, activate or delete another worker's state as part of
an implementation handoff.

For authorized helpers, include the shared
[delegation and cleanup boundary](../SKILL.md#delegated-helpers-and-cleanup)
in each brief; perform any authorized deletion yourself after checking it.
