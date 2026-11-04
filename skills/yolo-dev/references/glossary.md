# Shared terms

| Term | Meaning |
|---|---|
| Ticket | One piece of work with a stable, short descriptive `<type>-<topic>` ID, outcome and owner. Examples are `feat-agent-metadata` and `fix-participant-panel`, not new assignments. |
| Stage / state | Stage is Plan → Build → Review → QA → Merge. State is separate: ⚪ unstarted, ▶️ active, ⛔ blocked, ⏸ postponed, ✅ completed. In recaps, put the icon before the stage (`▶️ Build`); a part is a child ticket row, never a stage. Completed means that stage completed. Cancelled outcomes go to BOARD with their reason, not a made-up stage. |
| Plan | Agreed scope, acceptance criteria, boundaries and QA strategy, owned by the planning architect. |
| Build / binary build | Build stage means implementation. A binary build means compiling outputs. |
| Candidate | Exact proposed source content, identified by commit/tree or complete patch hash, not a moving branch name alone. |
| Artifact / bundle | An artifact is a patch, report, log or binary. A bundle is an identified set of built yoloctl companions linked to source provenance. |
| QA | Execution of the architect's validation strategy. Source review alone is not executed QA. |
| Gate | A condition for advancing, not automatically another permission request. |
| Handoff / receipt | A handoff assigns the next action/owner. A completion receipt identifies result, candidate, evidence and limits; transport acceptance alone is not completion. |
| Executor / authority | Executor is the actual box/session/identity performing an operation. Authority is its effective authorized scope; a role assignment does not grant it. |
| Evidence / attestation | Evidence supports a claim. An attestation is an attributed report, not automatically an inference or independent verification. A hash proves artifact identity, not every claim inside it. |
| Frozen | An identified review artifact is preserved; corrections get a new identity. Reopen only affected acceptance, not every unchanged stage. |
| Slot | Named ownership of a competing shared build/runtime resource. Unrelated source work need not wait. |
| Merge / activation | Merge integrates accepted source. Activation selects a build for a running environment; it is separate. |
| Blocked / postponed / hold | Blocked cannot take its next step; postponed is deliberately outside active workflow until a stated condition; hold explicitly forbids proceeding and names its issuer/release authority. A user hold needs user release unless delegated. |
| Unknown / unsupported / deferred | Unknown lacks sufficient evidence; unsupported has an established mechanism/capability boundary; deferred coverage was intentionally left unrun. None means PASS. |

## Importance and urgency

**Importance** is material contribution to agreed goals, correctness or
reliability, including dependencies. Task size/effort does not determine it.
**Urgency** requires a real deadline or concrete consequence of delaying
attention. Importance alone does not make work urgent.

| Important | Urgent | Recommended treatment |
|---|---|---|
| Yes | Yes | Prioritize for the next authorized work |
| Yes | No | Schedule |
| No | Yes | Delegate, batch or minimize within scope |
| No | No | Backlog; revisit when relevant |

The coordinator recommends; the user decides. Keep recommended and user-confirmed
classifications separate, with a short reason and urgency's deadline/delay
consequence. Unknown remains unclassified. **Backlog** names only the bottom
quadrant, not the whole BOARD. Priority never overrides a hold or grants work.

**Related-ticket grouping** is a recommendation naming tickets, a shared
dependency/root cause/overlap/reusable setup, expected benefit and tradeoff.
The user decides; grouping does not silently combine scope or erase separate
acceptance criteria.

**Conventional Commits** use `<type>[optional scope]: <clear summary>`, for
example `feat: collect and display agent metadata`. Use the same type vocabulary
for ticket slugs; keep IDs stable across BOARD/PROGRESS moves.
