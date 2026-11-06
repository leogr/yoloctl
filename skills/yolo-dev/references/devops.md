# DevOps

Execute the architect's validation strategy, integrate accepted work and close
task resources. Use the applicable installed operator/AIRC skills and
[AGENTS.md](../../../AGENTS.md). Verify the actual executor and authorized
scope; a role, copied CLI or mounted socket alone is not a grant.

An in-box DevOps executor must have the `yolo.space.operator` feature and
current effective operator grant to control the development environment, and
use the installed `yolo-box-operator` skill. The feature requires operator
authority; the skill explains its use and grants none. An outside executor uses
verified host authority for the selected host context and operation instead.
If authority is missing, return that blocker to the coordinator for authorized
provisioning; never substitute copied credentials or an authority bypass.

## Prepare the environment and execute QA

Use the coordinator-arranged privileged `<privileged-dev-box>` for nested
runtime QA. Ask the user for its name if it has not been supplied; the fallback
is `privileged-dev`. [Initial privileged launch requires the host](../../../docs/digests/privileged-boxes.md);
an operator can use an appropriately verified running one within its supported
authority. Confirm only prerequisites needed by the operation: source/bundle,
toolchain, space/box identity, native storage/capacity, and usable nested KVM
when the test needs it. Read-only source checks and host builds need no blanket
privilege/KVM gate. Never test by mutating the active coding environment.

Use supported native paths for VM/account databases and necessary caches; put
durable development evidence in canonical `.yolo-team/tasks/<ticket>/` and
record external paths/ownership. Preserve live bind paths. Consult
[share boundaries](../../../docs/digests/vm-shares.md) and
[host storage](../../../docs/digests/host-storage.md) when relevant. A historical
free-space observation is not a current budget check.

Claim the competing build/runtime slot through the coordinator. Execute the
finite QA plan on the identified candidate; do not add a campaign. Capture
command, environment relevant to the result, stdout/stderr and actual final
exit status. Preserve incomplete/unknown outcomes rather than reconstructing
success from an internal receipt. Existing reliable harness records suffice;
no bespoke runner or manifest ceremony is required for every check.

Before a nested VM/smoke run, claim the coordinator's quiet-build slot and
verify approximately 7 GB or more free on the shared space disk (or the QA
plan's larger requirement). During long runs actively inspect free disk, VM
process state and log growth; do not rely solely on a completion notification.
A storage-paused VM may remain alive without progress. On a pause or exhausted
budget, preserve evidence and coordinate recovery; do not launch more fixtures.

Do not kill/stop a `box use` or `box attach` client mid-call to enforce a test
budget. Use supported bounded `--timeout`/`--wait-timeout` operations, write
output to a file before parsing it, and inspect an interrupted result rather
than replaying it. This is an execution discipline, not a claim that client
loss still fences the space; see [owned attach setup](../../../docs/digests/attach-setup.md).
After each run, retain evidence and remove only its exact disposable fixtures
once consumers are gone. After final Go checks, run `go clean -cache`; never
substitute a broad shared-disk purge.

Report source coverage, binary provenance and runtime coverage separately.
If building companions, check their bundle/identity and the media/init
compatibility required by the plan; a new build is not permission for image
assembly or activation. Stop on a failed/unknown boundary, retain the evidence
and ask the planning architect whether to fix or replan. Never blindly replay
an uncertain operation. Release the slot with the real outcome and any retained
runtime dependency, not just a statement that the command ended.

## Integrate and close

After plan/review/QA acceptance, verify the target branch's current head and
clean/known state, the actual candidate content and the intended linear parent.
Composition changes need affected review/checks; clean application alone is not
semantic acceptance. Run the repository-required pre-commit checks and KB review
from AGENTS.md. Reuse applicable unchanged evidence; do not rerun solely for
status, and do not waive required gates without explicit user direction.

Use Conventional Commit subjects, `<type>[optional scope]: <clear summary>`,
aligned with the ticket's `<type>-<topic>` terminology (feat, fix, docs,
refactor, perf, test, build, ci or chore). There is no mandatory one-ticket/
one-commit mapping. Integrate with `git merge --ff-only` into the agreed branch
(default `main`); record prior/new heads and resulting candidate identity.
No implicit public push, tag, activation or session restart.
The user publishes main with `bash scripts/push-main-checked.sh` instead of
`git push origin main`. It fast-forwards the remote main only after the CI
run of a draft `pre-main` pull request passes on Linux and macOS for that
exact commit; on a red run it lists the failing jobs and leaves main
untouched (pre-main and the draft pull request may have changed). If the
final push reports a failure, it reads main back and says what it found,
without retrying.

Give the user concrete rebuild instructions for the accepted source and actual
host platform, naming any companion/media prerequisites and deferred coverage.
Separate activation instructions and retain interactive host prompts; do not
put an interactive controller update in a heredoc or add automatic restart flags.
Record recovery options when relevant, without automatically resetting/reverting
history or restoring runtime state.

After completion and validation, clean only authorized task resources whose
unique work, evidence and consumers have been checked. Remove an exact finished
worktree from a namespace that can resolve its registration; do not prune all
worktrees. Retained failures, active data disks, account state, credentials and
another ticket's inputs are not generic scratch. Report kept resources and
their dependencies. Move the ticket's compact closed outcome through the
coordinator; never silently erase its evidence or pending state.

Follow the shared [cleanup boundary](../SKILL.md#delegated-helpers-and-cleanup):
no wildcard `rm`, including on your own fixtures or output directories. Use a
new output folder for each run; retain evidence before exact-path cleanup.
