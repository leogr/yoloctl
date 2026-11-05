# CLI selection and availability

[FACT] Host and in-box operator clients use the same Cobra box definitions,
flags, aliases and completion callbacks. Explicit space flags/positionals beat
host context; operator scope is fixed to its own space. `box create NAME` chooses
the space before resolving account defaults. `box run` and root `run`/`ps` are
unavailable; use named `box create`, `box attach` and `box list`. [1](#source-1) [2](#source-2)

[FACT] New space names, including import destinations, use 1–16 ASCII
characters: an alphanumeric first, then alphanumeric, dot, underscore or hyphen.
Existing longer names remain usable. New macOS Lima spaces use
`~/.config/yoloctl/lima`; each records its home in the bootstrap, while absent
fields preserve the legacy location. Explicit home overrides remain supported;
Linux storage selection is unchanged. No automatic migration occurs.
[20](#source-20)

[FACT] Box names use 2–63 ASCII characters: an alphanumeric first, then
alphanumerics, `.`, `_` or `-`, excluding reserved infrastructure names.
Create and copy reject invalid names before controller selection, including
`--manifest-only`; manifest validation enforces the same box rule. Other entity
kinds retain their one-character minimum. Existing one-character box declarations
fail validation; they are never silently renamed or deleted. [1](#source-1)

[FACT] `box create --operator` is a creation-time convenience: it adds the
operator and agent chat features and defaults networking to unconfined.
Explicit network/workdir choices and extra features survive; the stored
declaration has no expanding operator role or automatic chat enrollment.
The same shared flag is available through shell and palette completion.
[5](#source-5)

[FACT] VM backend completion lists adapters linked into the host build without
probing a VM. Prepared-image arguments complete host directories. These
callbacks cover both space creation and import. [1](#source-1)

[FACT] For Lima, `space create`, `space import`, `space start` and `space recreate`
reject a socket path that is too long for the selected home and space name,
before VM/disk effects or recreation teardown. The diagnostic gives the computed
path, byte count and strict platform limit; choose a shorter name or a shorter
home for a new space. Reads and cleanup retain their existing behavior.
[19](#source-19)

[FACT] Host-only `space set SPACE --memory auto|SIZE --cpus N` and
`space edit SPACE` change VM RAM/CPU settings only while the VM is verified
stopped. Omitted set flags retain their saved values; `auto` recomputes concrete
RAM, while CPU count remains explicit. `space create --cpus` defaults to `auto`
and saves a concrete count; set/edit never reselect it. `space create --disk`
defaults to `auto` (half of the free host space, 30–50 GiB); `space set --disk`
and edit's `disk:` only grow it, saved for the next `space recreate`, and
show/list name a saved size not yet applied [14](#source-14). `--data-disk`
on create and set (with `--no-backup`) sizes and grows the data disk; a
grow finishes at the next start and is recorded at the next stop
[15](#source-15). [12](#source-12)
Edit exposes only a flat `cpus`/`memory` YAML document. Editor waits release the lifecycle lock;
saving rechecks the original bootstrap and stopped state. Neither command boots
or restarts a VM. Desired settings survive backend-application errors; the next
explicit start synchronizes them before booting. [10](#source-10)

[FACT] Box/account/runtime commands require a running matching controller.
Offline `space list/show` provide boot/VM metadata, with unavailable declarations
identified explicitly. Aggregation reports unavailable spaces and returns a
nonzero partial-result status rather than presenting them as empty. Read views
never boot the VM. [1](#source-1)

[FACT] Shared resolution separates declaration, runtime, session and drift.
A reachable controller may report uncertain container runtime; destructive
inventories must instead fail on uncertainty. JSON contracts are command-specific;
exit codes preserve remote child status and otherwise use tagged-user/generic
classification. [1](#source-1)

[FACT] `box attach --box-use` (also `attach --box-use`) accepts versioned JSON
Lines interaction through a separate managed frontend. It is mutually exclusive
with `--passthrough` and shares the existing controller authorization and attach
lifecycle. Human and agent attachments remain interactive peers. [3](#source-3)

[FACT] `box use BOX ACTION` is a finite convenience over that same attachment:
screen, history, text, key, wait or a bounded JSONL batch. It retains structured
identity/results, stops the batch on rejection and detaches afterward. Wait
uses this attachment's observation baseline; it does not infer completion.
The command can start a missing session or stopped box, with autoupdate skipped
by default. [3](#source-3)

[FACT] Read-only `box session BOX [--json]` shows the conversation record: the
assigned ID and how it was assigned (chosen, learned or switched), the last
observed conversation when it differs (the next start resumes it), whether the
agent reports in-app switches, launch, resume and observed evidence, and the
conversations it replaced. JSON adds `assigned_source`, `current` and
`selection` under `schema_version` 1. [13](#source-13) Observed evidence
includes the agent's reported state and a context of unknown size as tokens
used; it has no activity fallback. [16](#source-16)

[FACT] `space view` is a host-only live view of one running space. It reads
through one long-lived internal stream after the usual bundle and running-VM
check, so it never starts a VM. It refuses box operators and shows unavailable
inventories as errors. Its detail popups render the matching command's `--json`
output instead of a separate view of the same data. A RUNNING dot pulses on managed terminal output or on CPU/disk load sustained for 3 s, never on input, network or brief load; unproven activity is dimmed rather than shown idle. [4](#source-4) [11](#source-11)
[INFERENCE] A viewer resize usually makes the application redraw, which is real
PTY output and briefly reads as activity. [11](#source-11)

[FACT] Host-only `chat` ensures the owned server and opens the native human
view. `chat ensure/status/enroll/revoke` are finite typed controller operations
available to same-space operators as well;
`status --json` distinguishes desired generation, server readiness and pending
participant binds. Enrollment requires explicit recreation to change existing
binds, and revocation takes a box name (its one active membership) or an
immutable member ID. None of these commands boots
a VM or adopts an unrelated server box. Required AIRC interfaces are probed;
an unsupported image fails without a TCP fallback. `chat connect/disconnect BOX`
acts on one named participant client, independently of enrollment or an agent
wake listener. Self-enrollment returns pending binds; self `--recreate` is
rejected before changes. Host-only `chat nickname NICK` preserves
authentication and saves the next connection's display nickname; reconnect the
human view explicitly. Box lifecycle role synchronization preserves published
changes as pending when the managed chat holder is stopped and control is
absent/refused; `chat ensure` explicitly starts it. Verified live orphan servers
still receive role updates. [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] `chat enroll BOX --connect [--client space] [--recreate]` sequences
enrollment and named-client connection without retry. Flags are validated
first; connection failure retains membership and reports partial success.
`--client` requires `--connect`; JSON is one combined result. Omitting
`--connect` preserves enrollment behavior. Agents still arm their own wake
listeners. [8](#source-8)

[FACT] Chat enroll, connect/disconnect and revoke complete their declared-box,
current enrolled-box and immutable-member arguments through the bounded native
completion worker. Host and operator clients retain the same explicit authority
and fixed-space rules; unavailable controllers produce no fallback suggestions.
Shell and palette use the same Cobra endpoint. [1](#source-1)

[FACT] `chat channel list/create/add/remove` inspect or change the bounded native
catalog and per-agent channel permissions. Add/remove select an enrolled box's
immutable identity; desired changes and verified runtime application are distinct.
Ordinary list observes runtime application; `list --catalog-only` instead reads
one desired manifest and labels server application unobserved. Plain `chat`
uses that single-read catalog for refresh and pre-JOIN; `--catalog-read=observed`
selects the existing observed route, with no fallback. Invalid values refuse
before RPC. [9](#source-9)
The server must support channel-addition reload; these commands never restart
agents or auto-join clients. An interrupted result needs inspection. [9](#source-9)

[DERIVED] Review validation, space selection, controller authorization, resolver
depth, writers and completion together. A correct host-only RunE change is
incomplete if helper dispatch, unknown outcomes or server validation differ.
[1](#source-1) [2](#source-2)

[FACT] Image preparation reports immediately, streams plain BuildKit logs to
stderr for original and joined clients, and reports quiet work every two seconds.
Image builds may share unfinished preparation while each command keeps
its own application receipt. Ctrl-C releases a private attempt when its last
requester leaves; cancellation is best effort, so leftover work may still use
VM resources. There is no preparation acknowledgment. A typed interruption
before both job and request permission can offer `retry? [y/N]` on a terminal;
only an explicit yes sends a new request. Ordinary solve failures retain their
build error without this retry shortcut. A pre-Solve connection failure reports
not started. Wait notices name a verified lock holder
or tracked image job when known; stale owners fall back to a generic notice.
[17](#source-17)

[FACT] Hidden host-only `install` verifies complete releases and asks before
upgrades; `--yes` accepts a managed upgrade and never resolves unknown
conflicts. It never runs a space command: it prints each space's update or
optional recreate command for the user to run. At a terminal (or with
`--add-to-path`/`--completion`) it can append a marked PATH or completion block
to the shell's startup file. [18](#source-18)

[FACT] Read-only `version` and `version --json` report the build channel and
resolved config/cache roots without creating the Lima home. The external Lima
version probe is bounded. Ordinary commands retain startup setup. [18](#source-18)

## Sources

1. <a id="source-1"></a> [CLI, output and completion evidence](../digests/cli-state.md).
2. <a id="source-2"></a> [Dispatch and transport evidence](../digests/controller.md).
3. <a id="source-3"></a> [Box-use request/observation contract](../digests/box-use.md).
4. <a id="source-4"></a> [Live space view](../digests/space-view.md).
5. <a id="source-5"></a> [Operator preset and authority](../digests/operator.md).
6. <a id="source-6"></a> [Managed chat runtime and stream](../digests/chat-runtime.md).
7. <a id="source-7"></a> [Managed roles, settings and attachment](../digests/chat-settings.md).
8. <a id="source-8"></a> [Operator chat commands and limits](../digests/operator-chat.md).
9. <a id="source-9"></a> [Managed channel catalog and stream](../digests/chat-channels.md).
10. <a id="source-10"></a> [Stopped-space resources and recovery](../digests/resources.md).
11. <a id="source-11"></a> [Observed box activity](../digests/box-activity.md).
12. <a id="source-12"></a> [Automatic VM CPUs](../digests/vm-cpus.md).
13. <a id="source-13"></a> [Following agent conversation switches](../digests/conversation-following.md).
14. <a id="source-14"></a> [VM system disk size](../digests/vm-disk-size.md).
15. <a id="source-15"></a> [Data disk size and offline grow](../digests/data-disk-grow.md).
16. <a id="source-16"></a> [Agent state and used-only context](../digests/agent-state.md).

17. <a id="source-17"></a> [Image preparation and interactive retry](../digests/image-preparation.md).

18. <a id="source-18"></a> [Installation, build identity and host homes](../digests/installation.md).

19. <a id="source-19"></a> [VM adapters and Lima socket bounds](../digests/vm-adapters.md).

20. <a id="source-20"></a> [Lima storage and names](../digests/lima-storage.md).
