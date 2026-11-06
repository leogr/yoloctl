# Session persistence boundaries

[FACT] Linux and macOS hosts install a Linux build of the full CLI into the
VM as the session holder. Bundles verify the guest-selected amd64/arm64 artifact
against hashes linked into the host. Standalone Linux builds can use themselves
when compatible with the guest; macOS uses the Linux companions. Explicit space start/update installs the selected bundle before the controller
admits commands. Every attach requires that matching controller; active holders
keep their own executable inode and independent lifetime.
[1](#source-1)

[FACT] A VM-side holder owns the managed agent SSH child and PTY independently
of attach clients. Detaching a client preserves a living holder/session; later
attachment restores native terminal state and joins ordered live events. Holder loss cannot
adopt the existing child. Markers/tombstones distinguish known completion from
uncertain loss. [1](#source-1) Once an attach's setup is authorized, a client
that leaves or is signalled cannot cancel it: the holder may finish starting
afterwards, and a client timeout is no proof of a failed launch; inspect
`box session` or the request ID instead of retrying. After the attachment
starts, one signal is forwarded to it and ends it (the ordinary detach, status
0); a second ends the client unconditionally. [16](#source-16)
[INFERENCE] Inside a box the bundled sandbox's signal broker delivers a
mediated `kill` before answering it, and its listener lets a signal interrupt
and restart that call, so one `kill` can arrive twice. The attach-signal
regression has occasionally seen a single test signal reach the client twice
there, ending it with status 2 instead of the forwarded detach; no yoloctl
sender was found and nothing is changed. The mechanism and the unpatched
listener flags are recorded in [22](#source-22).

[FACT] A fresh session in an existing container refuses verified recipe-image
drift before launch, with explicit inspect/recreate guidance. Existing live
holders remain attachable; inspection failure is not evidence of stale image,
and genuine agent exit statuses are preserved. [20](#source-20)

[FACT] New sessions receive the launching host's bounded terminal name/version
for application preferences. Reattaching preserves the running process's original
environment; terminal identity does not change VT capability/query ownership.
[10](#source-10)

[FACT] Multiple attachments remain interactive; any client's resize applies.
Each viewer has a bounded output queue and independent writer. A slow or
disconnected viewer cannot hold the output lock or stall healthy viewers;
overflow closes its transport rather than silently dropping terminal bytes.
Final output precedes the known exit status within a shared shutdown budget.
[5](#source-5)

[FACT] A box whose agent declares conversation identity (Claude) owns its
conversation by ID instead of the shared account's last one: a new session
starts with a fresh ID and later sessions resume it, unless
`agent_session.resume` is `never` or `manual`. Launches bind to their holder
by a launch token; a launch with no evidence is uncertain and never replayed,
and only `--new-session` replaces a conversation. When the agent reports an
in-app switch (`/clear`, `/resume`, fork), the next start resumes the last
observed conversation, never a guessed current selection; a resume that exits
nonzero without a report is retried, never replaced by a fresh one. A verified container
replacement by recreate, edit, set, reconcile or chat `--recreate` keeps a
launched conversation resumable; a policy-only restart does not. Resume is reported
unverified until the agent says otherwise. A Claude resume keeps the effort and model last
reported by that same conversation (`--effort`, `--model`, after the box's own
args) while a carried value is available, overriding a later box edit until
`--new-session`; without one the box arguments apply, and for every agent the
box model/effort arguments seed a new conversation. Records stay on the space's disk
across space recreate but are not exported; an imported box starts a new
tracked conversation. `box session NAME` shows a record read-only; an
undecidable holder probe shows as unknown, never idle. [12](#source-12)
[17](#source-17) [21](#source-21)

[FACT] Default interactive attachment uses the shared renderer, including a
standalone view without tabs/palette. Full native snapshots preserve screen,
history and input state; only the holder answers terminal queries. Effects
such as bells and clipboard writes occur live, never from snapshot restoration.
`--passthrough` offers a limited, query-filtered UTF-8 transcript using the host's
renderer. Its bounded replay cannot reconstruct a complete old screen; opaque
extensions are omitted. Both modes can coexist without duplicate query replies.
Both report a locally invalid zero-sized grid with its measured dimensions and
advice to set a nonzero terminal size and attach again; they preserve received
exit statuses and distinguish genuine holder-stream loss.
[5](#source-5) [6](#source-6)

[FACT] `box attach --box-use` adds a structured managed frontend alongside
interactive and passthrough viewers. Agents request bounded text observations,
wait for changes and send text/keys into the same session. Advisory peer-input
activity supports collaboration without ownership locks or priorities. This
view follows canonical geometry and exposes no desktop integrations.
[8](#source-8)

[FACT] `box use` opens the same structured attachment for finite requests and
batches, preserving session/client identity and peer observations before
detaching. Observation revisions belong to an attachment; finite waits obtain
a local baseline instead of borrowing a revision from an earlier invocation.
`sent` means the holder queued all of that attachment's input bytes, not that
the application read or executed them. [8](#source-8)

[FACT] Session control sockets/state are outside agent-mounted paths. The holder
rewrites titles for all clients and filters clipboard writes out of replay.
Title templates and agent icons are chosen at holder start. Templates containing
`{agent-icon}` remove a matching leading agent icon before composition.
An initial title uses the template with an empty `{title}` before output or
attachment; later agent updates replace it. `{title}` alone disables this seed.
[1](#source-1)

[FACT] A read-only stats request reports the holder's PTY output and accepted
input counters for `space view`. It requires the exact build and admits no
viewer, so sampling never changes the session. [11](#source-11)

[FACT] The private protocol also carries bounded user-initiated PNG uploads
and single-view browser requests. The holder uses confined SSH channels to
create guest temporary files/helpers; no session socket is exposed to the
workload. Host approval/clipboard reads stay in the managed frontend, outside
VT replay and snapshots. Passthrough remains terminal-only. [7](#source-7)

[FACT] Runtime stop/recreate must clean holder and session artifacts consistently.
A TUI tab owns a local CLI subprocess; closing an attach tab detaches, while
an arbitrary exec/editor tab does not inherit a persistent-agent guarantee.
[2](#source-2) [3](#source-3)

[DERIVED] Three observations establish different things: container running,
holder/agent live, and persisted history readable by a new process. Do not use
successful replay as proof of storage integrity. The native-state smoke has a
separate fresh-process history oracle. [1](#source-1) [4](#source-4)

[FACT] An agentless session runs `session_command` when configured, otherwise
the login shell. Changes take effect on a subsequent session start. Normal
yoloctl detach preserves such a workload, for example `aircd`, through the
existing holder.
[9](#source-9)

[FACT] The packaged AIRC Codex adapter delivers batches through one persistent
subscriber. Its default queues input for a later turn; a separately selected
app-server mode starts idle input or steers the exact active turn, preserving
submission uncertainty without replay. Approval routing remains untested. Plain
wait/read remain available.
[9](#source-9)

[FACT] Agent boxes bind a capped per-box tmpfs at `/run/yoloctl/agent`, with one
sandbox-owned directory per launch generation. Hook and status line reports
written there are agent-reported evidence read without following links, with
health shown separately; container creation binds it and old containers stay
unobserved. [13](#source-13)

[DERIVED] A report under `gen-N` means "written into launch N's directory",
not "written by launch N's agent": the generation is a copyable environment
string and the directory is writable by every sandbox-UID process.
[13](#source-13)

[FACT] Each tracked launch of an observable box carries its generation, and
the current launch's reports are applied before the next launch is planned:
only main-session reports of that generation decide the resume outcome, once,
set model, effort, version, context and state, set the last observed conversation
and record a divergence shown as a switch. A missing report never
blocks a launch, but an unverifiable bound root or a foreign bind fails closed
before dispatch. Views show the live launch's reports read-only.
[14](#source-14)

[FACT] A profile's reporter (`session.observe.push`) is one of its required
features, so every box of that agent composes it and an existing box gains it
with its next recreate; Claude's is a managed status line, which also runs the
user's own status line, plus async identity and state hooks. Claude reports
working/idle, permission/input/dialog waits and API failures through those
hooks; unknown notifications report nothing. Print-mode Claude sessions
report no model, effort or context [15](#source-15). agy's reporter is the status line in the
account's own settings; its profile is observe-only, so every agy start is a
new conversation tracked only for its reports [18](#source-18).

[FACT] A managed launch is the lifetime of the background daemons its agent
profile declares (`session.daemons`; Codex's app-server): the holder stops
them before the agent starts and after an observed exit, never on a holder
stop. A declared daemon a user started from `box exec` is stopped too.
[19](#source-19)

## Sources

1. <a id="source-1"></a> [Holder, protocol and replay evidence](../digests/sessions.md).
2. <a id="source-2"></a> [Session-aware teardown](../digests/lifecycle.md).
3. <a id="source-3"></a> [Local terminal child lifecycle](../digests/terminal-rendering.md).
4. <a id="source-4"></a> [Persisted-history regression](../digests/account-storage.md).
5. <a id="source-5"></a> [Authoritative terminal and compatibility limits](../digests/session-replay.md).

6. <a id="source-6"></a> [Managed frontend and private transport](../digests/session-views.md).
7. <a id="source-7"></a> [Host integration boundary](../digests/host-integrations.md).
8. <a id="source-8"></a> [Collaborative box-use interface](../digests/box-use.md).
9. <a id="source-9"></a> [AIRC session lifecycle](../digests/airc.md).

10. <a id="source-10"></a> [Terminal identity and selection preferences](../digests/terminal-selection.md).
11. <a id="source-11"></a> [Observed box activity](../digests/box-activity.md).
12. <a id="source-12"></a> [Agent conversation identity](../digests/agent-conversations.md).
13. <a id="source-13"></a> [Agent observation substrate](../digests/agent-observation.md).
14. <a id="source-14"></a> [Agent reports in conversation tracking](../digests/agent-reports.md).
15. <a id="source-15"></a> [Claude status line and hook reporter](../digests/claude-reporter.md).
16. <a id="source-16"></a> [Owned attach setup and client loss](../digests/attach-setup.md).
17. <a id="source-17"></a> [Following agent conversation switches](../digests/conversation-following.md).
18. <a id="source-18"></a> [Antigravity status line reporter](../digests/agy-observation.md).
19. <a id="source-19"></a> [Agent daemon lifetime](../digests/agent-daemons.md).

20. <a id="source-20"></a> [Session remedies](../digests/session-remedies.md) — fresh-session image guard.
21. <a id="source-21"></a> [Tracked agent launches and settlement](../digests/agent-launches.md).
22. <a id="source-22"></a> [OpenShell compatibility](../digests/openshell-compatibility.md) — mediated signal delivery and listener flags.
