# AIRC in yoloctl

AIRC is maintained in [its own Rust repository](https://github.com/leogr/airc).
The `yolo.chat.skill` feature installs its persistent named/JSONL agent client, server binary and
collaboration skill. It grants no network access, operator authority or accounts.
Humans use the native view of the space's managed room. The Repartee client, its
`airc-box` launcher and their `yolo.chat.server` and `yolo.chat.repartee` features
were removed: a declaration naming either feature fails with an explanation until
it is edited. See the [implementation evidence](digests/airc.md)
and the pinned [upstream contracts](digests/airc-upstream.md).

## Space-owned managed room

On the host, `yoloctl chat` inspects the space's durable room and opens its native
human view. A healthy room opens without repeating setup; first use or a
confirmed missing runtime performs setup, including recreating a deliberately
deleted reserved chat box. An ambiguous runtime observation
requires inspection before an explicit `chat ensure`.
It needs a running matching controller and an AIRC image with Unix listeners,
ACL reload/revocation, JSONL v2 receipts and presence support. The bundled source
pin provides these interfaces; existing boxes need an explicit image rebuild
and recreation to install them. Unsupported feature probes stop with an error,
without falling back to TCP. See the [runtime contract](digests/chat-runtime.md)
and the [pinned source evidence](digests/airc-upstream.md).

The managed human stream additionally requires the `client-topic` and
`nick-regain` features.
`chat channel` mutations require `channel-add-reload` on the running server.
The bundled pin provides both; older packaged images may lack these interfaces
and are rejected explicitly. Text and images sent as one message need
`message-attachments` in both the chat server image and each participant's
image; rebuild them together, and until then images keep going as separate
messages ([staged attachments](digests/airc-attachments.md)).
Source-level feature checks are not proof that an existing box has them.
Image publication also requires `artifact-cli` and `artifact-metadata` in the
running image; rebuilding yoloctl alone does not update that image.

```sh
yoloctl chat ensure --space dev --box chat --human person
yoloctl chat enroll coder --space dev
yoloctl chat status --space dev --json
# Review pending binds; this explicitly replaces the participant container:
yoloctl box recreate coder --space dev --no-attach
yoloctl chat --space dev
# A box name revokes its one active membership, even after the box was
# destroyed; a member ID from status revokes that specific membership:
yoloctl chat revoke coder --space dev
```

In the view, Enter sends and Shift+Enter, Alt+Enter or Ctrl+J adds a line; pasted text never
sends. `/help` lists every key and what it acts on. Typing `@` suggests
participants (agents wake only for an explicit `@name` or `@all`), the
Participants panel (Ctrl+P) shows roles, explicit `airc away`/`back` state and
box activity, dragging over messages
selects and copies their text, clicking a box participant attaches its session,
Alt+↑ selects a message for reply/copy/link actions, Copy reference copies the
selected message's shared `m:` reference, the wheel or PgUp scrolls
and loads older history, `/settings` changes the nickname, Ctrl+G or `/channels` switches
channels (`/join NAME` and `/part` follow or leave one, each keeping its own draft),
and Ctrl+C twice quits. After a disconnect the view stays open and keeps the draft
but cannot send: quit, rerun `yoloctl chat`, and check history before resending
anything marked uncertain.

A shared `m:` reference in a message, or `msgid:` and the full ID, names one
message for every client: clicking it shows that message if it is loaded in the
same channel; otherwise the view says so, and PgUp may load it. Nothing is
fetched or guessed. A number such as `@289` in an older message is plain text
and cannot be followed. An agent's `airc send --reply` reaches the view as a
reply label that jumps the same way. The packaged AIRC named client uses the
same shared references for its records, `reply=` targets, confirmed sends,
`show` and `--reply`, so a reference copied from any client or from this view
names the same message. It refuses local `@N` arguments. Numbers written in
older messages stay plain text, and a pending delivery batch or `read --again`
saved by an older client keeps its saved bytes. Boxes whose images predate this
pin keep `@N` until rebuilt ([references](digests/chat-references.md)).

The room defaults to `#space`. Server history/artifacts and per-member credentials
live on the native space disk; server recreation preserves them. Import creates
independent room/member identities and fresh credentials. An unrelated existing
server box is never adopted. Enrollment never silently restarts an agent.
Live revocation requires a successful server reload and observation; retained
private token files are recovery state, not evidence of loaded ACL enforcement.

Use `yoloctl chat channel list --space dev --json` to inspect the channel catalog,
immutable ACL identities and applied status. `chat channel create build` creates
`#build` for current members; later enrollments receive only `#space` until an
explicit `chat channel add coder build`. `chat channel remove coder build`
removes channel access without deleting room enrollment, history or credentials.
All commands accept `--space`; mutation replies report desired versus applied
state. Clients join separately, and no agent is restarted. After a failure,
inspect pending state before explicit recovery; never blindly repeat an uncertain
mutation. See the [channel contract](digests/chat-channels.md).

An explicit `/join` refreshes the trusted catalog before its one JOIN request.
The view waits for authenticated membership confirmation before allowing sends.
Create an unknown channel with `yoloctl chat channel create NAME` first.
Authenticated humans and currently authorized space operators receive real
channel operator mode when joining; `@` reflects server role evidence.

Enrolled agents discover their private config in `YOLOCTL_CHAT_CONFIG` and may
explicitly connect a named AIRC client with it. The adjacent private `identity.json`
maps authenticated accounts to the human/member identities; nicknames are display
text. The human view uses a separate confined client and closing it leaves the
server running. Its initial history is a bounded tail; PgUp or upward wheel
scrolling at the top requests older server history. There is no automatic
reconnect or resend. Operator boxes use their own enrolled AIRC client.
`yolo.space.operator` grants same-space `chat ensure/status/enroll/revoke` and
`chat connect/disconnect` administration; human view/nickname and credentials
remain host-only. First operator setup needs explicit `--human NAME`.
The human view registers the saved nickname, and one human session holds it at
a time. If another session of your account is connected, a new view asks
first. **Take over** (Enter) closes that session with "Session taken over";
anything it was still sending becomes uncertain, and it does not reconnect.
**Cancel** closes the new view. A different account holding the nickname is an
ordinary registration failure (433).

An operator can enroll and connect in one command:

```sh
yoloctl chat enroll worker --connect --client space --recreate
```

`--client` defaults to `space` and requires `--connect`. Options are validated
before enrollment; connection runs only after enrollment is acknowledged.
If connection fails, enrollment remains saved and the command reports partial
success without retrying either operation. With `--json`, the shortcut emits
one result containing `enrollment` and either `connection` or `connection_error`.
Omitting `--connect` preserves the existing enrollment output and behavior.
The agent remains responsible for reading messages and establishing its own
delivery consumer or waiter through the installed AIRC skill.

### Enrollment by participant state

Declare every setting before the first launch: mounts, features (including
`yolo.chat.skill`), capabilities and agent args. Then use the shortest action
for the participant's current state:

| State | Next action |
|---|---|
| No declaration | Create the full declaration first, for example `box create ... --manifest-only --no-attach`; no account or box is created implicitly. |
| Declared, no container | `chat enroll BOX --connect --client space --recreate` materializes it through the shared create path (source-derived; live acceptance pending). |
| Running, new enrollment or bind drift | The same combined command, with explicit replacement intent and a saved continuity handoff. |
| Running, enrolled and bound; client stopped | `chat connect BOX --client space`; no replacement. |
| Matching client connected | Keep it. Inspect `airc status` in the box; repeating `connect` is refused, not an ensure. |
| Stopped, enrolled, in sync | `box start BOX --no-attach`, then connect. `--recreate` leaves an in-sync stopped container stopped. |
| Operator enrolling itself | Settings first, self-enroll without `--recreate` last, then peer replacement below. |
| Missing or stopped privileged box | Host launch; an operator may only continue a verified running privileged launch. |
| Unknown runtime, identity or binds, or an uncertain reply | Stop the sequence and inspect; never infer absence or retry from an error. |

Pass `--mount` an absolute host source. An operator's `/workdir` alias works
only under its own granted binds; an unmapped alias is refused, which does not
prove that a host grant is missing. `--manifest-only` saves the declaration
and skips the live-grant preflight, so it is not a read-only probe.

Standalone `yoloctl chat connect worker --client space` uses the participant's
private config and default box-local AIRC state. `chat disconnect worker
--client space` stops only that named client, preserving enrollment, credentials
and recovery state; other clients may remain connected. Neither command starts
or recreates a box, sends messages, arms a wake listener or reconnects
automatically. Only explicit `--recreate` replaces a participant container;
omit it when its current binds are already ready. Inspect after an uncertain
reply before taking another action.

Connecting a named client does not activate the agent conversation. Instruct the
agent to read the installed skill and retain one supported wake listener. A
provider's supported thread queue or an explicit `box use` input action can carry
that instruction; input delivery alone does not prove the agent has read it or
started listening. Do not start a second provider process on an active session
as a substitute for a supported wake interface.

Self-enrollment without `--recreate` returns pending binds. Self `--recreate`
is rejected before changes; a peer operator can explicitly recreate that box
from its independent persistent session after the reply. A synchronous command
chain rooted in the caller can also lose its reply when the caller is replaced.
Operators cannot exec/attach/use the managed server's human terminal. Finite
server lifecycle remains available and skips incidental attachment. See the
[operator chat boundary](digests/operator-chat.md).

## A manual AIRC server box

The managed room above is the supported human path. For agent-only experiments, a
box can also run `aircd` itself. Choose a **private host directory within the
space's existing mount grants**. It holds server configuration, all demo
credentials, history and artifacts; do not share that directory with
participating agent boxes. These commands use the selected running space:

```sh
yoloctl box create room --agent none --account none \
  --features yolo.chat.skill --workdir /path/to/private-chat-directory \
  --session-command /bin/sh --session-command=-c \
  --session-command 'umask 077; exec aircd serve --config .airc/server.toml' \
  --no-attach
yoloctl box exec room -- aircd init .airc
yoloctl box attach room
```

Initialization refuses an existing directory. It generates private files for a
loopback-only demo with `human`, `agent-a`, `agent-b`, and channel `#work`; private
messages are disabled. Each account's `.toml` file is an `airc --config` client
configuration. The attached session shows the server's log; detach normally to
leave it running. For a disposable demo, replace `--workdir PATH` with
`--workdir-mode ephemeral`. Ephemeral storage is lost on box destruction or VM
replacement. Stopping/restarting the box stops the server. A subsequent attachment
starts the declared command again; server configuration and database must still
exist. `--no-attach` creates or starts the container but does not start this
managed session.

`session_command` is an argv array for **agentless** boxes. Repeat
`--session-command` once per argument; commas and spaces are literal, with no
shell expansion. It is non-secret manifest data. `box set` replaces it for the
next session start; `--session-command=` clears it. Copy preserves it unless
switching to a real agent. Use `box exec BOX -- COMMAND` for a separate one-off
command. Managed, passthrough and box-use attachment keep their existing
semantics.

## Server behavior

The pinned AIRC server keeps operator-managed persistent channel bans. A mask such
as `*!agent-a@airc` bans the authenticated `agent-a` account across nickname
changes. A ban blocks joins and unprivileged speaking, including artifact uploads.
Existing members remain joined unless kicked; configured artifact download access
remains. See the [pinned source evidence](digests/airc.md#source-16) for the
exact revision.

The server also answers `USERHOST` queries from human clients with authenticated
account identities. Unknown nicknames are omitted; channel operators are not
reported as IRC server operators.

AIRC is a standalone project; yoloctl supplies optional packaging and confinement.
The server uses schema 4 and upgrades schemas 1–3 while retaining
existing messages, topics and files. Schema 3 permits stored multiline text and
schema 4 adds staged attachments; older AIRC binaries refuse a newer schema, so
the upgrade is forward-only. As the [source evidence](digests/airc.md#source-22)
describes, keep a backup taken before the first start of the new server if you
need to roll back; for a managed room that is a `space export` with its
workloads stopped. A backup taken after that start cannot restore an older
pin, and restoring it loses anything written since.

## Participating agents and remote transport

Install `--features yolo.chat.skill` alongside an agent's existing features. Supply only
that participant's client configuration and token file, with private permissions
and grants for those paths. The skill is installed at:

- `/etc/codex/skills/airc` for Codex;
- `/etc/claude-code/.claude/skills/airc` for Claude;
- `/home/sandbox/.agents/skills/airc` for shared discovery.

The canonical skill is `/usr/local/share/airc/skill/SKILL.md`; clients with other
discovery conventions can load it explicitly. Start a named connection once,
then use ordinary shell-tool calls:

```sh
airc connect work --config /path/to/client.toml
airc read work --wait 30
airc send work '#work' 'My finding, with the relevant evidence.'
airc status work
airc disconnect work
```

The local client stays connected between calls without a Python adapter or an
idle expiry. Read before speaking so humans and agents can steer the discussion;
typing alone does not end the wait. A send succeeds only after a matching server
echo. After interrupted output, `read --again` repeats the last saved read;
inspect `status` and history before retrying an uncertain send. Reconnect is
explicit and never resends messages automatically. Local state and history are
bounded and report gaps. State defaults to `$XDG_STATE_HOME/airc` or
`~/.local/state/airc`; choose a private box-local `--state-dir` when it should
survive container recreation. Container-home state is not account-native data.
The original `airc --config PATH` JSONL interface remains available with
persistent stdin/stdout pipes. Finite artifact commands use authenticated HTTP
without opening a second IRC connection:

```sh
airc artifact download --config "$YOLOCTL_CHAT_CONFIG" ARTIFACT_UUID ./chosen.png
airc artifact upload --config "$YOLOCTL_CHAT_CONFIG" --channel '#space' \
  --name chosen.png --stdin < ./chosen.png
```

Use a new download destination in a private directory. Named message headers
carry server-reserved artifact UUID, filename, byte count and SHA-256; compare
the downloaded bytes/hash to those fields. Typed URLs and filenames in ordinary
messages do not establish an attachment's origin or media type. Use the agent
harness's actual image-reading tool to inspect a supported image; downloading a
file alone does not prove visual inspection.

In the native human view, explicit Ctrl+V can prepare a PNG for intentional
publication. `seams.clipboard: false` disables the host clipboard read. Managed
images are limited to 4 MiB and share the room's artifact retention (32 per
channel). Upload requires a currently joined speaking session. A lost reply can
follow publication: inspect the request receipt and authenticated channel
history, and never automatically repeat the upload. See
[image transport](digests/chat-images.md).

For Codex, keep one retained subscriber job for the current named client and
thread. The packaged `airc-codex` adapter queues the delivered message batch
directly, with no per-wake read, rearm or Python bookkeeping:

```sh
: "${CODEX_THREAD_ID:?current Codex thread identity is required}"
airc subscribe work --wake mentions -- airc-codex --thread "$CODEX_THREAD_ID"
```

This matches the installed Codex recipe. Optional `--min-interval S` sets the
minimum seconds between accepted batches (critical events bypass it); 0 (the
default) and 15 are both supported.

Run it as one yielded/background harness job, retain the handle, and omit shell
`&`. The harness must keep it running after turn-end. An idle Codex thread starts
a turn for the batch; a busy thread handles it in a later turn. The header
preserves delivery/session identity and the records preserve authenticated sender,
reply and artifact metadata. Records remain external chat, not instructions from
the person running Codex. Fetch context only when needed: `airc show work m:…`,
`--parent` or bounded `--chain N`; a normal read remains available.

Delivery and manual reads use separate cursors. One consumer is allowed per
client. A failed or interrupted handoff leaves a pending UUID and stops the
subscriber; restart refuses to replay it. Inspect `airc deliveries work --show ID`
and the Codex queue before explicitly retrying, marking accepted or dropping that
ID. Acceptance proves transport receipt, not agent processing. The adapter uses
literal argv, caps the whole payload at 64 KiB and exposes it in the process list
while `codex queue` runs. Keep the default 8 KiB batch budget.

An opt-in alternative holds each batch until the thread is idle instead of
queuing it while the thread is busy. Use it only when the person running Codex
asks for it; the recipe above stays the default and nothing enables it
automatically:

```sh
airc subscribe work --wake mentions --sink-timeout 0 -- \
  airc-codex --thread "$CODEX_THREAD_ID" --wait-for-idle
```

The installed Codex recipe's "Optional wait for idle" section is the contract.
`--sink-timeout 0` removes only the subscriber's deadline for its adapter (the
default stays 60 s). The adapter reads the thread's status, read-only, from the
local Codex app-server socket under `$CODEX_HOME/app-server-control`
(codex-cli 0.164.0) and queues once after a fresh `idle`. Mentions arriving
meanwhile wait in AIRC and follow in order: a busy-period burst that fits
`--max-bytes` arrives as one further turn after the held batch's. This is
batching, not a queue cap; a turn can still start between the idle read and the
queue call.

A missing or dangling socket keeps the same batch waiting (retries after 1, 2,
4, 8, 15, then every 30 s); `--idle-timeout SECONDS` bounds the whole wait. A
Codex launch without a local daemon socket (`--profile`, or `-c` overrides
beyond a few feature and UI keys), or whose socket alias another box on the same
account replaced, therefore waits where the default recipe would deliver: use
the default recipe and never change launch settings to make it pass. A thread
that is not loaded or in error, a foreign endpoint, a malformed answer, an
expired idle timeout or a signal exits 3 with the batch pending; a signal during
the queue call exits 4, uncertain. Recover as above. Do not combine it with the
receipt hold. See [AIRC upstream contracts](digests/airc-upstream.md).

A separate opt-in mode delivers into an active Codex turn instead of queueing
behind it. The default queue recipe and optional idle-wait recipe stay available.
Use only a verified existing daemon socket for this TUI and its exact loaded
thread; do not guess an endpoint, create a daemon or change session permissions.

```sh
: "${CODEX_THREAD_ID:?current Codex thread identity is required}"
: "${AIRC_CODEX_SOCKET:?verified existing Codex daemon socket is required}"
airc subscribe work --wake mentions --sink-timeout 0 -- \
  airc-codex --thread "$CODEX_THREAD_ID" --app-server \
  --app-server-socket "$AIRC_CODEX_SOCKET"
```

The installed recipe's "Optional active-turn delivery" section defines the
binding and recovery. The socket must be private to the current user under
`<canonical /tmp>/codex-daemon-<euid>`; the adapter verifies the peer, exact
thread and current active turn ID. Idle uses `turn/start`; busy uses
`turn/steer` with that expected ID. It never falls back to queue, forces an
interrupt, replays a command or sends execution-setting overrides. Acceptance
may occur during a long tool call while model processing follows its completion;
there is no response deadline. Approval routing remains untested.

AIRC still owns one pending batch and one consumer. A separate private journal
under that named client's state directory persists submission identity before
the RPC and the receipt before acceptance. Accepted replays settle without a
second submission; in-flight or corrupt records stop even an explicit retry.
Inspect the batch, its journal and the conversation before `--mark-accepted ID`
or `--drop ID`. Never delete uncertainty to resend it. The journal retains up to
65,536 records, refusing new submissions when full; retire only inspected,
settled IDs that will never be retried. Other Codex versions and approval pauses
need validation before selecting this mode. Installation changes no listener.

For Claude Code, `yolo.chat.skill` installs AIRC's
`airc-delivery` plugin at `/usr/local/share/airc/claude-plugin`. The image-level
drop-in `/etc/claude-code/managed-settings.d/50-airc-delivery.json` enables it.
That drop-in sets only `CLAUDE_CODE_PLUGIN_DIRS` and `AIRC_DELIVERY_SESSION=space`,
and nothing is written to the shared account leaf. The plugin's monitor waits
until the managed `space` client is connected, then runs
`airc subscribe space --wake mentions --min-interval 15 --max-bytes 2560 --wrap 480 -- -`.
Each batch reaches the session as a monitor notification, with no per-wake read,
rearm or helper call. Box `env` is not visible to the agent session, so it
cannot select the wake mode. A coordinator sets `AIRC_DELIVERY_WAKE=messages`
through a Claude settings `env` block instead: managed settings, user settings
or `--settings`. Prefer `--settings '{"env":{"AIRC_DELIVERY_WAKE":"messages"}}'`
in that box's agent extra args. User settings live in the shared account leaf
and would apply to every box using that account.
`CLAUDE_CODE_PLUGIN_DIRS` needs Claude Code 2.1.280 or later. Monitors run
only in interactive sessions: a non-interactive `claude -p` session gets no
delivery and uses the wait recipe. Claude Code cuts a notification
line near 500 characters and an event near 3000; the batch bound and `| ` line
wrapping keep a batch inside both. If `subscribe` exits because its
connection ended, the monitor prints one `AIRC delivery waiting: …` line and
subscribes again with the same settings once the session is connected again;
it never connects itself. Any other exit, such as a pending batch or another
consumer in the same connection, prints one `AIRC delivery stopped: …` line;
then use the wait recipe until the next session start.
The installed `references/claude.md` gives the recipe and recovery.

For other harnesses, `airc wait work --deliver --wake mentions` returns the
triggering batch. A plain `airc wait work` waits for unread messages and critical
events without consuming them or replacing `read --again`. Use `--wake mentions`
for addressed messages or `--wake all` for all retained events. Channel messages
wait for two seconds of quiet, up to ten seconds per burst; critical events and
buffer pressure bypass that delay. Every event remains readable regardless of
the wake mode. `airc away work REASON` and `airc back work` explicitly set and
clear this connection's presence; waiting never changes it. After a plain wait
reports readiness, read/respond and arm one waiter again. `--deliver` supplies
the batch directly; fetch context only when needed. `wait` has no overall timeout unless
`--timeout SECONDS` is supplied; disconnects and errors end it. The agent harness
must either await it in an active turn or support a background
completion that starts another turn. Verify the latter before promising
unattended responses. A connected client alone only buffers messages.

`airc status` reports the worker's current readiness registration as armed,
none, or unknown. Its `since` and `deadline` describe one bounded internal wait,
which an unlimited wait renews; they do not promise a future model turn. The
subscriber instead has separate `delivery:` and `delivery consumer:` status;
`waiter: none` does not mean a registered subscriber stopped. The
installed `scripts/airc-monitor.py` helper records one retained harness job
handle and its observed completion. It replaces repeated inline state-file
editing, but neither launches a listener nor verifies that a model will wake.
See `references/monitor.md` beside the installed skill for its commands.

The helper above is for legacy wait recipes; the Codex and Claude subscribers do
not use it. Direct delivery was checked in one Codex 0.163.0 TUI/Sol-low configuration,
including a queued follow-up during a running shell command and no receive/rearm
tool calls. Other harnesses, reconnect/failure cases and the rebuilt feature image
were not established by that live check. See the installed `references/codex.md`
beside `SKILL.md` for the adapter's exact recovery contract and coverage limits.

The pin also supports optional scoped delivery receipts. A Codex subscriber
can enable `--receipt-timeout SECONDS --receipt-scope "$CODEX_THREAD_ID"` without
`--wrap`; the installed skill describes the exact notice and one-time receipt
report. This paces later batches after transport acceptance; it does not drain
or cap an existing provider queue, prove processing, or make retries safe.

This release retains local IPC version 2 and normally uses saved state version 3.
Saving a delivery receipt request upgrades that client's state to version 4;
clients that never enable receipts stay on version 3. Older binaries refuse
newer saved state. Before upgrading an existing named client, explicitly disconnect it using its
old binary, then reconnect with
the new binary and the same state directory. That explicit connect upgrades
version 1 or 2 state; older binaries then refuse it. `airc join`, `airc part` and
`airc topic` each wait for server confirmation under a fresh operation ID and
are never retried; after a lost reply, inspect `airc status` for that ID.
These named-client versions are separate from the server's schema upgrade
described above.

The generated localhost addresses apply **inside the server box**. Cross-box
routing, TLS termination and credential distribution remain explicit deployment
steps. Configure the server listeners and each client's host/port and artifact
origin using [AIRC's operations guide](https://github.com/leogr/airc/blob/main/docs/operations.md).
Keep the server's full configuration and other participants' tokens private.

A confined participant must use AIRC's explicit `proxy = "http://HOST:PORT"`
client setting. Set it to that box's OpenShell `HTTPS_PROXY`/`HTTP_PROXY` endpoint;
IRC uses CONNECT and artifact HTTP uses the same proxy. A refused connection fails
without a direct fallback. IRC does not implicitly inherit proxy environment
variables. TLS still verifies the destination; configure `ca_certificate` for
the intended trust chain. Installing AIRC does not add an endpoint grant.

On the host, create a custom capability (for example, copy an existing network
capability with `yoloctl capability copy`, then use `yoloctl capability edit`).
Its grants should name the exact server endpoint and AIRC executable:

```yaml
schema_version: 1
name: network.airc
description: Shared collaboration server
grants:
  network:
    egress:
      - hosts: ["chat.example.com"]
        ports: [6697, 443]
        binaries: [/usr/local/bin/airc]
```

Grant it with `--capability network.airc` when creating the participant box.
Do not grant `yolo.space.operator` merely to participate in chat. Published ports alone
do not configure listeners or TLS. A server reachable on the VM's container
network currently needs the explicit `--unconfined-network` setting to bind
outside OpenShell's inner network namespace. That removes its network allow-list;
it keeps the confined process/filesystem launch. The two participant boxes in
the smoke retain network confinement and receive no operator authority.

The disposable cross-box smoke uses explicit plaintext on the VM bridge with no
published ports. It exercises the routing and proxy policy but does not establish
an Internet-facing TLS deployment. Real deployments must configure TLS for IRC
and HTTPS for artifacts, including the intended proxy trust behavior.

## Maintenance and validation

Image builds download the published AIRC release archive for the build
architecture (`airc-linux-amd64.tar.gz` or `airc-linux-arm64.tar.gz`) from
GitHub, verify the SHA-256 recorded in
`internal/features/airc-source/manifest.json` before extraction, and install
its static binaries, skill, Claude plugin and license. Nothing is compiled and
no AIRC source is embedded in yoloctl. No Rust toolchain enters the host build
or the image.

The selected release (v0.1.1, source `6af217cf`) includes rustls-webpki 0.103.16 and webpki-roots 1.0.10:
external TLS/HTTPS rejects compromised emSign intermediate keys even when a
configured CA permits the chain. Unrelated private CAs remain supported.
This updates AIRC's compiled verifier, independently of VM registry tools or
OpenShell. The new pin affects future image builds; existing boxes require an
explicit rebuild/recreate when you choose to adopt it. See
[CA and verifier evidence](digests/trust-stores.md).

To update the pin, choose a published AIRC release, check both architecture
archives against its `SHA256SUMS` asset, and record the release tag, its source
commit and each archive's URL and SHA-256 in that manifest. Then run:

```sh
make check 'PKG=./assets/... ./cmd/... ./internal/...'
make smoke-airc
```

`make smoke-airc` builds in a disposable space and exercises two real agent clients
and the human account's client, human steering, history after the human
reconnects, server shutdown, the installed revision against the pin, installed skills, the absence of the removed client
and launcher, named-client non-consuming readiness/read replay, a confirmed send
and reply named by shared `m:` reference (decoded to the observed msgid, used by
`show`, and found again in status after reconnect),
and a managed session running `aircd` with detach/rejoin and command revival after
box restart. A second
phase provisions a server and two separate confined participant boxes, checks
executable-scoped proxy denial, reconnect history, artifact transfer and history/
artifact persistence after server restart. It requires
a host with the selected smoke backend: Lima by default, or Linux amd64 with
usable KVM for `SMOKE_BACKEND=firecracker make smoke-airc`. The smoke also checks
that the composed image already owns its home/npm tree with the space's UID/GID.
Its nested PTY driver explicitly grants `fs.pty.allocate`;
normal attachment uses the holder's allocated PTY without that grant. It does
not use provider credentials or touch an existing user space.
Set `SMOKE_KEEP_ON_FAILURE=1` to retain a failed test's disposable space and
build cache. Rerunning with the same `SMOKE_ROOT` explicitly updates that
disposable space's controller to the current build and resets its smoke boxes;
a successful run cleans up unless `SMOKE_PRESERVE_SPACE=1` is also set.

The local runtime driver can also run with `aircd` and `airc` on
PATH: `python3 scripts/smoke/airc-runtime.py`. Passing this local process test is
not a composed-image or OpenShell runtime result. The isolated VM smoke is required before claiming deployment readiness; remote
TLS topology remains separately validated.
