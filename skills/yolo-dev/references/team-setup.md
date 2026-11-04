# Team setup

The coordinator's recipe for adding chat-enabled agents to the current space.
It complements the installed operator and AIRC skills; command details and the
[state table](../../../docs/airc.md#enrollment-by-participant-state) live in the
[AIRC guide](../../../docs/airc.md). Check each command's `--help` against the
build you are actually running.

## Default team preset

Use this roster as the default when setting up this team; a different team or
model assignment overrides it explicitly. It defines roles and intended model/
effort, not runtime proof or authority. Verify the current CLI's model support
and observe the launched session; never copy shared account settings to force
these choices. Keep current membership and quota in team tracking, not here.

Ask the user for box names; if none are supplied, use `model-role`, adding a
numeric suffix for multiple boxes with the same model and role. The bracketed
names below are role placeholders, not literal CLI arguments. Keep actual box
names in private team tracking; do not publish them in this skill or examples.

| Role | Box/member | Agent | Model | Effort |
|---|---|---|---|---|
| Coordinator | `<coordinator-box>` | claude | claude-opus-5-5 | high |
| Architect | `<claude-architect-box>` | claude | claude-fable-5-1 | xhigh |
| Architect | `<codex-architect-box>` | codex | gpt-6-astra | high |
| Dev | `<claude-dev-box-1>`, `<claude-dev-box-2>`, `<claude-dev-box-3>` | claude | claude-opus-5-5 | high |
| Dev | `<codex-dev-box>` | codex | gpt-6.1-sol | xhigh |
| DevOps / QA | `<devops-box>` | claude | claude-opus-5-5 | medium |
| Researcher | `<researcher-box>` | agy | Gemini 4 Argon | medium |

The agy model is a display name; resolve its supported selector through the
installed harness before launching. These defaults select neither an account
nor an authentication method. "Another Opus" means another dev using the Opus
dev row; it does not mean another architect. Extra capacity still needs an
assignment and the setup checks below.

Infrastructure is separate: `<chat-server-box>` is the chat server;
`<privileged-dev-box>` is the agentless/accountless privileged QA executor.
Ask the user for those names too; without supplied names, use `chat-server`
and `privileged-dev`, respectively. Personal boxes and retired members are
not part of this default worker roster. Recheck membership rather than
reviving a retired box from an old example.

## Shape each box

Grants are explicit task decisions. Do not infer them from a role name.
For this team's chat boxes, declare `--unconfined-network` before first
materialization, independently of whether the box is an operator. Dev and
architect boxes also declare `fs.pty.allocate` at setup so local PTY checks run.
These team defaults do not authorize changing existing boxes, widen operator
authority, or apply to confined test fixtures. A different team must explicitly
override the defaults it changes.

| Purpose | Box shape |
|---|---|
| Coordinator, or DevOps managing peers | Real agent + declared account; `--operator` only when those operations are authorized. The preset grants full box authority and, unless disabled, open networking. |
| Architect, dev or researcher worker | Real agent + declared account + `yolo.chat.skill`; no operator grant merely for development or chat. |
| `<privileged-dev-box>` privileged QA executor | `--agent none --account none --privileged` with the required tools; no chat or operator enrollment. The host owns its first launch. |

Three setup steps are host-only: launching a privileged box when no running
privileged runtime can be continued, adding space mount grants, and the human
chat view/nickname. This is not the complete authorization list: account and
space management and controller-recovery acknowledgment are also host-owned.
Enrollment and peer lifecycle are ordinary operator operations; see the
[operator contract](../../../docs/digests/operator.md) and
[privileged boxes](../../../docs/digests/privileged-boxes.md).

## Settings first, launch last

| Stage | Check at the decision point | Record |
|---|---|---|
| Inventory | Verify the CLI build/help and the requested role, model and effort. Inspect declarations, runtime and sessions with read-only JSON commands. | Existing identities and active work, not an inferred empty space. |
| Host prerequisites | **Never replace the sole peer carrying a continuation.** Verify canonical host sources and grant/live-share evidence before starting consumers. | The host action actually needed, or UNKNOWN. |
| Complete declaration | Choose the account/auth route and required tools; include `--unconfined-network` for team chat boxes and `fs.pty.allocate` for dev/architect boxes (also for other roles when needed). Put mounts, features and args in the first declaration or one `box set`. | Desired settings saved; credential presence is not login success. |
| Editor helper, if any | Verify the binary exists. `EDITOR` is one executable path and cannot carry arguments. | A concrete executable handoff; no regex YAML edits. |
| Enroll and connect | Use the shortest state-appropriate command below. Inspect a partial or uncertain outcome before another effect. | Saved membership, applied binds and named connection, separately. |
| Agent and listener | Observe the actual model/effort and tool versions. Follow only the applicable installed harness recipe. | Session observed; listener armed; wake observed or unqualified. |

Not every later change forces replacement. Image/feature, mount, workdir,
account, resource, network and operator changes recreate the container, and so
does a capability change that flips the workdir between read-only and writable
(`fs.workdir.write`). Other policy-only changes restart a confined box and leave
a proved privileged runtime running. Agent extra args apply at the next managed
session start.

`--agent-extra-args` is CSV. A value containing quotes or commas needs CSV
quoting, and `box set` replaces the whole suffix. This exact form was parsed by
the pinned flag library:

```sh
--agent-extra-args '--settings,"{""env"":{""AIRC_DELIVERY_WAKE"":""messages""}}"'
```

The unquoted JSON form is rejected with a `bare "` parse error. Use
`box use NAME key escape`, not `esc`; shell completion lists the native names.

### Mount sources

Pass `--mount` the absolute **host** source. An operator's in-box
`/workdir/...` alias is accepted only when it lies under one of its own granted
binds; an unbound alias fails with `operator path ... is not mapped to a granted
host directory`. That failure proves the alias is unmapped, not that a host
grant is missing. Operators cannot read space grants (`space.settings` and
`space.validate-grants` are host-only).

`box create --manifest-only` validates and **saves** a declaration. It skips the
live-grant preflight that runs at materialization. Use it only for a declaration
you intend to keep; never as a read-only probe or proof of a live share.
`box set` has no `--mount`; an existing box needs an explicitly reviewed
`box edit`, not an invented flag or generic regex tool.

## Add a new worker

After verifying the running controller, existing account and intended paths:

```sh
# First room setup only; later ensure omits --human and keeps the identity.
yoloctl chat ensure --human HUMAN
yoloctl box create worker --agent codex --account EXISTING_ACCOUNT \
  --workdir /absolute/host/project --features go,yolo.chat.skill \
  --unconfined-network --capability fs.pty.allocate \
  --mount /absolute/host/sibling:/workdir/absolute/host/sibling \
  --manifest-only --no-attach
yoloctl chat enroll worker --connect --client space --recreate --json
```

Add the mount only when the task needs it and its grant exists. Add the assigned
model/effort args and explicit capabilities. Materializing a never-started
declaration through `--recreate` is derived from source and still awaits live
acceptance. Then run `box use worker screen`; at an idle prompt, send **one**
onboarding instruction through one `box use worker batch --stdin` text+Enter or a
supported thread queue. Input `sent` is not READY. Do not prompt a busy
conversation or start a second agent with `box exec`.

## Enroll yourself through a peer

Self `chat enroll --recreate` is refused before any write. Sequence:

1. Save your handoff and exact conversation/thread ID.
2. Apply **all** settings, plus any explicitly authorized temporary resume args.
3. Self-enroll without `--recreate`, **last**. While chat binds are pending, a
   `box set` with valid changed settings can save them and then fail with
   `managed chat bindings changed; explicit box recreate required`. Unchanged
   values are not saved again, and validation errors fail before saving, so
   inspect the declaration rather than assume either outcome.
4. Confirm an authorized peer is onboarded and listening in its **independent
   persistent managed session**. A synchronous `box exec peer` chain still
   depends on your box.
5. The peer runs `box recreate SELF --force --no-attach` and
   `chat connect SELF --client space`, then waits for the resumed prompt in
   `box use SELF screen`.
6. The peer clears the temporary args only after verifying the intended
   session. Then it submits the saved continuation once.
7. The restored agent re-establishes and inspects its own listener.

If a set already failed, the peer inspects the saved declaration rather than
repeating it. Claude resumes its tracked conversation; a Codex exact-thread
resume needs explicit authorization and observation. See
[conversation identity](../../../docs/digests/agent-conversations.md).

## Onboarding instruction

Fill these fields: box/member and role; scope and holds; canonical repo, team
and sibling paths; model/effort; client `space` and channel; continuation
identity. Then add only the matching harness paragraph:

- **Codex:** Inspect `airc status space`; keep a correct connection. Read the
  installed Codex recipe, verify this thread's `CODEX_THREAD_ID`, and retain
  **one** `airc subscribe space --wake mentions -- airc-codex --thread
  "$CODEX_THREAD_ID"` job. After a container replacement the old job is gone:
  start it in the restored thread, never with a copied handle or thread ID.
- **Packaged interactive Claude:** Use client `space`, which packaging
  configures. Follow the installed Claude recipe: `airc status space` shows a
  registered `delivery consumer:` only after the client connects. Before that,
  the configured monitor may still be waiting, and status cannot show it. Never
  add a waiter, and never start the plugin from a tool. If the client stays
  connected with no consumer, report that. After a stop notice, use the
  recipe's wait fallback.
- **Other harnesses (including AGY):** Follow the installed generic recipe and
  retain one supported waiter. Report wake as armed only, or UNKNOWN, until
  turn-end wake is demonstrated for that harness.

Workers use `mentions`; the coordinator uses `messages`. For Claude, select it
with the launch `--settings` argv above, never box env or shared account
settings. Each instruction ends with: send one confirmed READY, then wait.

**READY** names the box/member and role, the observed settings and required
paths/tool versions, the connection and its delivery owner, and whether wake is
armed or observed. Input acceptance and dispatch receipts are not READY.
