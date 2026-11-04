# Agent reports in conversation tracking

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Each tracked launch prepares its generation directory after
allocation and before the dispatching write.

A container without the bind, an inspect that cannot run, or a
generation-level failure (a used directory, a full tmpfs without proof) starts
the launch without the variable. The record shows `unsupported` or
`unavailable` with the reason.

Two cases fail closed before dispatch, so no holder starts:

- an unverifiable root (`ErrRootUnverified`: missing, foreign, uncapped, or the
  wrong owner or mode);
- a bind that inspect reports foreign or read-only (`ErrObservationBind`).

The allocated launch is reused once the root or bind verifies.

On success the command gains `YOLOCTL_AGENT_GEN=N` and the record starts N at
`no-report`. Generation N-1's directory may be dropped for space only if that
launch was `closed` (same-token tombstone or fresh-sandbox proof). A drop is
noted as a gap. [1](#source-1) [2](#source-2)

[FACT] Before planning, `ensureAgentSession` applies the current launch's new
reports under the box lock, so the next resume requests the conversation the
agent last reported and a silent nonzero resume exit is settled as `failed`
([following switches](conversation-following.md)). [1](#source-1) [3](#source-3)

[FACT] `Record.Apply` takes only the current launch's generation.

- Subagent lines never touch the ID, outcome, model, effort, context or state.
- Lines at or before the applied high-water mark are skipped, so rereads and
  rewrites never re-stamp; changed content at earlier offsets only shows as a
  gap.
- The first main SessionStart naming a conversation decides the outcome once:
  `resumed` for source `resume` with the requested ID, otherwise `mismatch`.
- Every main SessionStart naming a conversation, except `compact`, sets the
  record's `current` conversation; a learn launch's first one also becomes the
  assigned conversation.
- The first main report of another conversation than the assigned one is kept
  as the launch's divergence, even after a return to the assigned ID. It is
  shown as a switch and refuses nothing; a new launch or retirement clears it.
- Hooks give the model on SessionStart, PostModelSwitch or Stop (Codex) and
  the effort on Stop (Claude). Every `statusline` line gives model, effort,
  version and context, the last two only from there; a Codex `query` line
  gives model, effort and state only while its `session_id` names the
  conversation folded so far (current, else assigned), and is dropped
  otherwise or when unlabelled. Context comes from any line carrying it. None
  touches the ID, outcome or divergence. Each value carries its source
  (`hook`, `statusline` or `query`), the agent's time and the read time.
  Absent fields keep the last value, unknown ones stay unknown, and a new
  launch starts empty.
- The latest reported state wins; a `statusline` or `query` line without one
  resolves a wait to `working` ([agent state](agent-state.md)).
  Claude reports `working` on UserPromptSubmit, `idle` on Stop unless tasks or
  crons remain, `failed` on StopFailure, and permission/input/dialog waits or
  idle from Notification. Its notifications are state-only; unknown and
  subagent-tagged notifications are omitted. Captured AskUserQuestion reports
  `permission_prompt`, so its mapped state is `waiting_permission`. [9](#source-9)
- Health is the worst read of the generation and never changes an outcome.
[4](#source-4) [5](#source-5)

[FACT] `box show`, `box list` and `box session` apply the current generation's
unread reports to their loaded copy at read time. They follow the same rules
and high-water mark, and never write or lock.

- `read_at` is the sampling time, so a reread is never a new event.
- `live` is `current`, or `unavailable: …`. That covers a failed read, and the
  command's 2 s budget, which holds because each command runs in its own
  worker process. The budget is cooperative: `observe.ReadWithin` checks its
  context between steps, so a read can overrun it by one step on the local
  tmpfs, and a hung kernel call is not bounded. Nothing cancels a syscall, and
  no reader outlives the call.
- A view-only outcome or divergence appears as `pending_outcome` or
  `diverged.pending`. It is agent-reported and lives only on the discarded
  copy until the next start's fold decides it.
- Live-holder joins that hold the space operation, including
  `ensureAgentSession`'s earliest return, fold and save under the box lock. A
  fast-path attach writes nothing.
[3](#source-3) [4](#source-4) [7](#source-7) [8](#source-8)

[FACT] The shared public view projects `observed` as `id`, `at`, `read_at`,
`source`, `generation`, `health`, `detail`, `live`, `pending_outcome`, `model`,
`effort` and `version` (`{value, source, at, read_at}`), `context`
(`{size, used, percent, source, at, read_at}`, each count only when reported),
`state` (`{kind, source, at, read_at}`) and `diverged`
(`{id, source, at, read_at, pending}`). Records written before
sources existed stay valid without one. The cursor and applied position never
appear. The summary line and the `box session` text share the notes, label
values as agent-reported (with their source), add the context and state, and
show only the current launch. [8](#source-8)

[FACT] The profile requires its reporter (`session.observe.push`, also in
`requires_features`; see the [Claude reporter](claude-reporter.md)), so no
create-time default or opt-in remains. A container created before the
observation bind records `unsupported` with the note that observation starts
with the box's next `yoloctl box recreate NAME`. [1](#source-1) [6](#source-6) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Launch wiring](../../cmd/yoloctl/session_observe.go#L1-L199) — `observationViewBudget` and the view reader, `foldLiveLaunch`, `unsupportedObservation`, `observationEnv`, `observeCurrentLaunch`.
2. <a id="source-2"></a> [Allocation order](../../cmd/yoloctl/session_identity.go#L279-L331) — `launch` computes the closed previous launch before `Allocate` and calls `observationEnv` (failing closed on an unverified root) before `Dispatch`; the never retirement and the never and --new-session retirement in `plan` at L169-L217.
3. <a id="source-3"></a> [Fold before plan](../../cmd/yoloctl/session_client.go#L504-L508) — `observeCurrentLaunch` precedes `plan`; `foldLiveLaunch` follows both `bindLiveLaunch` calls (L329, L374).
4. <a id="source-4"></a> [Apply](../../internal/agentsession/apply.go#L1-L283) — `ReadCurrentReports`, `foldForView`, `BeginObservation`, `ObservationCursor`, `Observable`, `Apply`, `Divergence`, `currentConversation`, `applyLine`; `Observed`, `Divergence`, `ObservedValue`, `ObservedContext`, `ObservedState` and the source checks in `Validate` in record.go.
5. <a id="source-5"></a> [Apply regressions](../../internal/agentsession/apply_test.go#L1-L497) — resumed, mismatch, generations, rereads and rewrites, subagents, preparation states, summary line, divergence, status line values, stored bounds, the Codex fold, the state fold.
6. <a id="source-6"></a> [Profile requirement](../../internal/agentprofile/profile.go#L164-L230) — `ObserveSpec`, `SelectionReport`, `PushFeature`; builtin/claude.yaml `requires_features` and `session.observe`.
7. <a id="source-7"></a> [Wiring regressions](../../cmd/yoloctl/session_observe_test.go#L1-L759) — bound and unobservable launches, previousGone, folded mismatch, the required reporter, resuming the reported conversation, failed resumes, never's fresh start, fail-closed root and bind, views without writes, other generations, live joins and the held ensure entry, box session text, slow live reads.
8. <a id="source-8"></a> [Public view](../../internal/agentsession/status.go#L82-L259) — `ObservedView`, `DivergenceView`, `ObservedValueView`, `ContextView`, `StateView`, `observedView`; the view fold in `Describe`, `notes`/`Lines` (box session text), `ContextView.text` and `Line`; key set in cmd/yoloctl/box_session_test.go `checkConversationShape`.
9. <a id="source-9"></a> [Claude state reporter](claude-reporter.md) — hook state mapping, Notification allowlist and subagent exclusion.
