---
name: yolo-box-operator
description: Inspect, create, run, or manage yoloctl boxes and administer the current space's managed chat for the user's authorized task.
---

Use the ordinary `yoloctl box ...` CLI. Start with `yoloctl box list --json`
and `yoloctl box inspect NAME`; use a subcommand's `--help` for its current
flags. The same commands work from the host and from an enabled operator box.

Inside a box, authority covers box operations and explicit managed chat
administration in the current running space.
It includes other existing accounts, stronger box capabilities, delegation
through `yolo.space.operator`, and destroying workloads. Use that authority only for
the user's task. It does not grant space, account, catalog, or host management.

- To create a related workspace, use `yoloctl box copy SOURCE NEWNAME --no-attach`.
  Copy duplicates settings, not files. Host workdirs remain shared; an ephemeral
  workdir starts empty. The selected account shares its native agent state.
- Use `--account NAME` only for an existing account appropriate to the task.
  There is no implicit account creation. Inspecting a box never proves an
  account's credentials are safe to expose.
- `--workdir .` and relative mount sources inside an operator box map through
  its actual project binds. Ephemeral or private account paths cannot become
  host shares. New host mount grants require the user on the host.
- Pass `--mount` the absolute host source. An in-box `/workdir/...` alias works
  only under your own granted binds; an unmapped alias is refused, which does
  not prove a grant is missing. You cannot read space grants. `--manifest-only`
  saves a declaration and skips the live-grant check, so it is not a probe.
- Use `yoloctl box exec NAME -- COMMAND ...` for a separate confined command.
  `box attach` joins the persistent agent session; it is not command execution.
  Prefer `--no-attach` for creation in automation.
- `box set` edits desired settings and reconciles them. `box edit` uses the
  caller's editor and rejects a save if the declaration changed meanwhile.
  A failed apply can leave the new settings saved: inspect the declaration,
  never assume rollback. Image, mount, workdir, account, resource, network and
  operator changes recreate the container, as does flipping `fs.workdir.write`;
  other policy-only changes restart a confined box, not a proved privileged
  one; agent extra args apply at the next managed session start. Declare everything before
  the first launch. `--agent-extra-args` is CSV: quote a value containing quotes
  or commas, for example `'--settings,"{""env"":{""K"":""v""}}"'`.
- A lost connection may leave an operation applied. Keep its reported request
  ID, inspect the target before retrying, and ask a host operator to query
  `yoloctl space operation SPACE ID`. Do not automatically repeat destructive
  or non-idempotent commands.
- Stopping a `box use`/`box attach` client, or closing its output, never pauses
  the space. Once its setup is authorized it finishes without the client and
  may start the session afterwards. A signal before the attachment starts
  ends the client with status 75; check `box session BOX` before retrying.
- Stopping or destroying your own box can cut off the reply. Revoking one
  operator does not undo boxes or delegated operators it already created.

If the controller reports a version mismatch or an unavailable space, report
its suggested host action. Do not modify infrastructure, disk files, or
account storage to work around it.

## Administer managed chat

Use the ordinary CLI from the operator box; routine setup and enrollment do not
need host commands. The selected controller must already be running and match
the helper build. Start with `yoloctl chat status --json`.

```sh
yoloctl chat ensure --human person
yoloctl chat enroll worker --connect --client space --recreate
yoloctl chat disconnect worker --client space
yoloctl chat revoke worker
```

The first setup needs the human's name explicitly. Subsequent ensure calls
retain that identity without `--human`. Enrollment creates durable membership
and adds the client feature. `--recreate` explicitly replaces the participant
container to install its private binds; private container-home data is lost.
Omit it to save membership and inspect pending binds before replacement. Revoke
takes a box name, which names its one active membership even after the box was
destroyed, or an immutable member ID from status (a 32-hex argument is always
an ID). It applies ACL removal and disconnects that member's clients; optional
`--recreate` removes retained binds.

`enroll --connect` connects the named client only after enrollment succeeds;
the default client is `space`. Client options are checked before enrollment.
If connection fails, enrollment remains saved: inspect the reported state
before another action. Neither step is retried. Without `--connect`, enrollment
has its existing behavior; `--client` requires `--connect`. The agent must still
establish its own delivery consumer or waiter using its installed AIRC skill.

For a new participant, declare every setting first, then enroll once:

```sh
yoloctl box create worker --agent codex --account EXISTING_ACCOUNT \
  --workdir /absolute/host/project --features go,yolo.chat.skill \
  --manifest-only --no-attach
yoloctl chat enroll worker --connect --client space --recreate --json
```

| Participant state | Next action |
|---|---|
| Declared, no container | The combined command materializes it (source-derived; live acceptance pending). |
| Running, new enrollment or bind drift | The combined command, with explicit replacement intent and a saved continuity handoff. |
| Running, enrolled and bound; client stopped | `chat connect BOX --client space`; no replacement. |
| Matching client connected | Keep it; repeating `connect` is refused, not an ensure. |
| Stopped, enrolled, in sync | `box start BOX --no-attach`, then connect. |
| Missing or stopped privileged box | Host launch only. |
| Unknown state or uncertain reply | Stop and inspect; never retry from an error alone. |

Connect/disconnect acts on one named AIRC client in a running enrolled box,
using the client's default box-local state directory. It never changes
membership, starts/recreates the box, sends messages or arms an agent wake
listener. Disconnect affects only the chosen client; another named client may
remain connected. Use the existing AIRC skill for reads, sends and one supported
delivery consumer or waiter. There is no automatic reconnect or retry after an uncertain reply.
Never copy tokens/configs between boxes or ask for the human credential.

Self-enrollment without `--recreate` returns safely with pending binds. Do it
**last**: while binds are pending, a `box set` with valid changed settings can
save them and then fail until an explicit recreate; inspect the declaration. Self `--recreate` is rejected before
changes. If this operator needs new binds,
another operator must recreate it after the enrollment reply. Use that peer's
independent persistent managed session; a synchronous `box exec peer` chain
still depends on the calling box and can lose its reply during recreation.

Human chat view/nickname and low-level recovery remain host-only. Operators
cannot exec, attach or use the managed server's terminal, which holds the human
identity and private control files. Finite server lifecycle commands work and
skip incidental attachment with a diagnostic. Use `chat ensure` to start and
apply the managed service, and ordinary participant clients for conversation.

## Interact with an existing agent

Use this procedure when the user's task calls for sending prompts or keystrokes
to another box's persistent agent session:

For finite operations, prefer the built-in helper:

```sh
yoloctl box use NAME screen
yoloctl box use NAME text "Please review this change"
yoloctl box use NAME text --stdin < prompt.txt
yoloctl box use NAME key enter
yoloctl box use NAME wait --wait-timeout 5s
yoloctl box use NAME history --limit 50
yoloctl box use NAME batch --stdin < requests.jsonl
```

It preserves JSON ready/session/client records and responses, then detaches.
It has attach's ensure semantics: it may start a session or restart a stopped
box. Agent autoupdate is skipped by default. `sent` means the session holder
queued the input, not that the agent read or executed it. Inspect before input
and after uncertain delivery; never blindly resend.
Wait obtains this attachment's baseline and reports changes/settling, not agent
completion. A timeout response still includes the current observation. Do not
reuse a revision from a different attachment.

Batch accepts 1..32 existing JSON requests (at most 256 KiB total), each with a
distinct correlation ID; blank/whitespace-only lines are rejected. All input is
read and validated before attaching;
requests run in order, stopping on the first rejection. An optional detach must
be last. Wait without `since` uses the batch's last screen/wait, or emits a fresh
baseline screen first. Batch is not atomic against another participant's input.
For example, a text request followed by key enter submits once on one attachment.
The default overall deadline is two minutes including session startup (`--timeout`,
at most ten minutes), plus bounded receipt cleanup. Printed per-action results
remain valid if attachment receipt cleanup later times out. No Python helper or
terminal PID handling is needed.

For ongoing response-driven interaction, use `yoloctl box attach NAME --box-use` with a long-lived subprocess and
stdin/stdout pipes. This managed frontend restores the shared screen locally and
returns compact JSON observations on request. The user and other agents can
stay attached and interact with the same session. It follows their geometry.
`--passthrough` remains a separate compatibility mode; do not combine the flags.

- Read the initial `ready` record (interface version 1, session and client IDs).
  Send one JSON request per line with a unique correlation `id` and an `op`:

  ```json
  {"id":"1","op":"screen"}
  {"id":"2","op":"text","text":"Reply with OK"}
  {"id":"3","op":"key","key":"enter"}
  {"id":"4","op":"wait","since":1,"timeout_ms":30000,"settle_ms":100}
  {"id":"5","op":"history","offset":0,"limit":50}
  {"id":"6","op":"detach"}
  ```

- Inspect the screen before sending input. `text` inserts text without Enter;
  `key` uses the session's current keyboard modes. Use `modifiers:["ctrl"]`
  with `key:"key_c"` only when an interrupt is intended. Multiline text requires
  the application's bracketed paste mode. Text is limited to 16 KiB.
- Pass the actual last screen/wait `revision` as `since` for changed rows.
  Apply rows by their zero-based index when `full:false`; replace the displayed
  window when `full:true`. Cursor and `start_row` use absolute screen indices.
  Responses cap text at 32 KiB and 200 rows and flag truncation. Fetch history
  only when needed; its offset counts backward from the newest retained line.
- Prefer bounded `wait` requests to repeated polling. A settled screen or
  timeout does not prove the agent finished. Read the resulting response.
  Commands are processed in order; a pending wait delays subsequent requests.
- `input.peer_revision` and `last_peer` report another attachment's activity,
  including agent peers. Inspect changed drafts/output and decide whether to
  hold or continue. These are advisory signals, not ownership or permission;
  simultaneous input can still mix. Do not infer another participant's intent
  from activity alone.
- `sent` acknowledges forwarding, not execution. IDs do not deduplicate actions.
  After uncertain delivery, inspect before sending again. `detach`, stdin EOF
  after queued requests, or process termination leaves the agent running;
  verify the session afterward. No PTY or PID lookup is needed for this mode.

When using the existing compatibility frontend, follow this PTY procedure:

1. Inspect the box and its session state. `box attach` can start a missing
   session and restart a stopped box; it is not a read-only viewer. Attachments
   share input and terminal geometry with other viewers.
2. Run `yoloctl box attach NAME --passthrough` in a long-lived terminal tool
   with a PTY. Retain the tool's session handle for input/output and identify
   the local attach process PID for cleanup; these are different identifiers.
   Passthrough is useful for captured terminal interaction. Default managed
   attachment remains preferred for human use and full screen restoration.
3. Drain the initial bounded replay, which can still be large and contain ANSI
   controls. Limit displayed output and inspect the current prompt or dialog
   before typing. Do not interrupt existing work unless the task requires it.
4. Send the prompt through the terminal handle, then send Enter (`\r`) separately.
   Inspect the result: the application may buffer pasted text or require an
   additional confirmation. Send another Enter only when the visible state
   calls for it. Read the fresh agent response; echoed input or replay alone
   does not prove success. Do not blindly resend a prompt after a timeout.
   For a connectivity test, request a unique acknowledgement without tools,
   file changes or additional agents.
5. Detach by sending SIGTERM to that exact local attach process
   (`kill -TERM PID`). Typing Ctrl+C into the PTY sends input to the agent and
   may interrupt its work. Do not exit the agent or stop the box to detach.
   Wait for the local client to exit, then verify with `box list --json` that
   the target session remains running.
