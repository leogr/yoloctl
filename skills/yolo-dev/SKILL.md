---
name: yolo-dev
description: Develop yoloctl on a host or inside yoloctl using assigned coordinator, architect, dev, researcher or DevOps roles. Covers team tracking, isolated source work, review, QA and integration; use for yoloctl development rather than ordinary use of the CLI.
---

# yoloctl development

Before following links, resolve the loaded `SKILL.md` to its physical path
(for example, Python `Path(loaded_skill_md).resolve(strict=True)`). Its parent
is `<checkout>/skills/yolo-dev`. Resolve this file's and every role/glossary
reference's links from their respective **physical file directories**, never
by normalizing `..` against the discovery symlink. Repository targets are
`<checkout>/AGENTS.md` and `<checkout>/docs/...`, not an account's global guide
or docs directory. If resolution or a target is unavailable, report that
boundary rather than substituting another file. This rule applies throughout
the references below.

Follow [AGENTS.md](../../AGENTS.md) for engineering, documentation and repository
checks. This skill supplies operations and role boundaries; it grants no runtime
authority. Install through its AGENTS.md bootstrap, checking shared installations
before creating a symlink. Read the [glossary](references/glossary.md) when a term
is unclear; **Build** is implementation, **binary build** is compilation.

## Enter the task

1. Identify the execution environment from available launch/context evidence:
   outside yoloctl, inside a box, or unknown. A directory prefix or missing
   `/run` paths does not prove host execution. Inspect only relevant non-secret
   context; never print credentials. For operations, consult the existing
   [operator contract](../../docs/digests/operator.md) and installed operator
   skill. A local CLI, socket or config file is a clue, not a current grant.
2. Take the role assigned by the user or authorized coordinator. If unclear,
   ask the **user** which role to perform; do not infer authority from a role.
   Load only that role's reference:

   | Role | Read when responsible for |
   |---|---|
   | [Coordinator](references/coordinator.md) | Team/chat/box management, tracking, dispatch and stage progression |
   | [Architect](references/architect.md) | Planning, challenge, review and QA strategy |
   | [Dev](references/dev.md) | Implementing an assigned plan in an isolated worktree |
   | [Researcher](references/researcher.md) | Research and authorized isolated feasibility PoCs |
   | [DevOps](references/devops.md) | Environment preparation, binary builds, QA, integration and cleanup |

3. Read the current ticket and linked plan. Establish its scope, owner, planning
   architect, next action and holds. Newer explicit user direction supersedes
   stale dispatches. Missing prerequisites block the affected action, not
   independent authorized source work. Verify operator/privilege/KVM only when
   the intended operation needs them; outside development need not use a VM.

## Shared working conventions

Use the coordinator-selected canonical `.yolo-team/` directory, including from
worktrees; do not start a competing team directory in each checkout. It is
Git-ignored. Put plans, notes, worktrees, PoCs and intermediate development
artifacts there. Existing evidence and live binds remain in place and linked.
Native VM/account databases, credentials and necessary native caches/runtime
storage stay in supported native locations; record their paths and ownership
in the task's detail. See [share boundaries](../../docs/digests/vm-shares.md).
Ignored does not mean disposable.

The coordinator owns exactly two main live documents:

- `PROGRESS.md`: tickets currently in Plan → Build → Review → QA → Merge,
  including queues and blockers.
- `BOARD.md`: work outside that workflow, using importance/urgency quadrants,
  plus postponed reasons and compact completed/cancelled outcomes.

Keep one authoritative ticket entry and move it with the same ID. Supporting
files under `tasks/<ticket>/` hold detail, not another live status index.
IDs are short descriptive `<type>-<topic>` slugs, such as
`docs-yolo-dev-skill`; use Conventional Commit types, not opaque counters or
hashes. Commit subjects use `<type>[optional scope]: <clear summary>`.
Examples are not assignments. The user decides priority and related-ticket
grouping; recommendations are recorded separately from confirmed decisions.

Use epistemic tags for significant claims in plans, notes and intermediate
reports: `[FACT]` with source or attribution, `[DERIVED]` with premises,
`[ASSUMPTION]`/`[UNKNOWN]` for unresolved evidence, and `[PROPOSAL]` or
`[DECISION — user]` for choices. Record assumptions but discard them as premises
for implementation, acceptance or destructive decisions. An attributed result
is not independently verified just because its report has a hash. Preserve
identified review artifacts and version corrections; reconsider only affected
acceptance, plus repository-required checks.

Improve guidance where a predictable mistake occurs: make the decision point,
required check and recovery clear, and verify the revised instruction against
the failing case. Saying "the rule already existed" does not remediate a failure.
Keep product contracts in the KB; this skill governs the team's workflow.

## Chat and handoffs

For an authorized shared conversation, use the **installed AIRC skill** and its
harness-specific delivery recipe. Coordinator wake policy is `messages`; all
other roles use `mentions` (including replies). Keep exactly one consumer or
waiter as that recipe requires. Do not add a waiter beside a plugin consumer.
Use the installed `yolo-box-operator` skill for authorized box/session work;
if required tooling is absent, report the boundary rather than inventing it.

Put every command you ask the human to run in chat in a fenced Markdown
code block, including one-line commands.

Dispatches and receipts identify the ticket/revision. Send one concise completion
receipt with candidate, result, evidence path, limits and next owner; avoid
acknowledgement chains and pasted reports. Inspect uncertain outcomes before
any retry. Save open tickets, holds and the last handled message reference
before a context/session transition; recover bounded chat context afterward.
When you answer in the chat, write nothing in your private session: no
repetition, summary or "answered in chat" note. Write privately only something
important that the chat does not carry (to save output tokens).
Transport acceptance is not task completion. Respect the issuer/release
authority of a hold; no historical assignment silently resumes held work.
Plans and reviews target parity with the existing behavior; a guarantee beyond
it becomes a requirement only after leogr approves it with its cost shown.

## Delegated helpers and cleanup

No agent, including the assigning main agent, runs `rm` with a wildcard, even
inside its own output directory. Write each run to a new folder (`run-2`,
`run-3`, and so on) instead of emptying a previous run's folder. When cleanup is
necessary and authorized, use exact named paths after the checks below.

When helper/subagent delegation is authorized, its brief must forbid `rm` and
other deletion or cleanup commands. The assigning main agent owns any cleanup:
verify ownership, retained evidence and live consumers, then use exact paths,
never a glob relative to an assumed working directory. Delegation does not
expand cleanup authority or transfer another worker's resources.
