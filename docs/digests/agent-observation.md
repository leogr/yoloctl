# Agent observation substrate

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; reader schema `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; query thread `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] Each agent box gets a tmpfs at
`/run/yoloctl/controller/agent-observe/<box>`. It sits in the reserved
controller namespace, outside the sandbox socket directory already bound into
the box. Caps: 2 MiB and 32 inodes, `nosuid,nodev,noexec`, root-owned 0755.

An existing mount is adopted only with exactly those caps and flags. Any other
mount there is refused and never unmounted. Root operations open the directory
without following links and check its filesystem type, owner and mode. Mounting
is Linux-only; host builds compile stubs. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Container creation mounts the root and binds it read-write at
`/run/yoloctl/agent`, for agent boxes only. A declared mount overlapping that
target is refused. Starting an existing container remounts the root first, but
only when the container already binds it from the expected read-write source;
a foreign bind is an error. Destroy unmounts and removes the root after the
container is gone. Inspect records the bind as `ObservationEnabled`
infrastructure, so neither its presence nor its absence is a recreate reason.
A container created earlier stays unobserved until its normal recreate.
[4](#source-4) [5](#source-5) [6](#source-6)

[FACT] `Prepare` creates `gen-<N>` for one launch with `mkdirat` on the root fd,
owned by the sandbox uid/gid with mode 0700. It reuses an existing directory
only if it is empty with that owner and mode. Only the controller can create
entries in the root.

Before creating, it removes obsolete canonical generations (`gen-<k>`, k < N-1)
by an fd-relative `unlinkat` walk that never follows links, bounded by the inode
cap. Future, non-canonical and unknown entries stay.

If creation then fails with ENOSPC or EDQUOT and the caller passes proof that
the previous holder is gone, `Prepare` removes `gen-<N-1>`, retries once and
returns `droppedPrevious`. A cursor into a removed generation that had consumed
reports reads as `gap`. Every failure wraps `ErrUnavailable`, which callers
report as observation health and never treat as a launch failure.
[2](#source-2) [7](#source-7)

[FACT] The `yolo.agent.observe` feature installs Claude's reporter: a managed
status line and async hooks on one writer (see the
[Claude reporter](claude-reporter.md)); Codex and agy features install the same
writer with their own launchers ([Codex](codex-observation.md),
[agy](agy-observation.md)). Its wrappers exec the interpreter the
build resolved. The build takes `/usr/local/bin/python3` or `/usr/bin/python3`
(3.8+ with the writer's modules, checked with `-I -S`), installs `python3` with
apt if neither works, and otherwise fails.

The writer:
- needs `YOLOCTL_AGENT_GEN` and an existing `gen-<N>`, and only ever opens
  directories, never creates them;
- waits at most 0.5 s for `flock` on `events.lock`, then drops the event;
- appends one line of at most 4 KiB per event to `events.<k>.jsonl`;
- starts the next file instead of exceeding 256 KiB and keeps two files;
- truncates short writes;
- stamps each line with `dur_ms`, its run time up to the write;
- always exits 0.

A subagent event's `session_id` can name a later conversation, so such lines
are only marked as subagent evidence.
[8](#source-8) [9](#source-9) [10](#source-10)

[FACT] `Read` opens `gen-<N>` without following links and requires the sandbox
owner. Any entry that is not a regular file makes the generation `invalid`;
unknown regular files are ignored and counted. Each `events.<k>.jsonl` is read
through `safepath` with `O_NONBLOCK` (a FIFO cannot block it) and must be a
single-link file owned by the sandbox, within 256 KiB.

Each line is decoded strictly against the writer's schema:
- canonical lowercase UUID session IDs;
- `source` only on SessionStart: startup, resume, fork, clear or compact;
- an enumerated effort (Claude's and Codex's named levels);
- a model of at most 128 printable bytes;
- an RFC 3339 timestamp, and `dur_ms` within 0–60 000;
- `wrapped` on `statusline` lines only, which alone may carry a version (32
  printable bytes) and a `handover` reason (`too-large` or `unavailable`)
  when not wrapped; a status line is never subagent evidence;
- context on `statusline`, `query` and PostModelRequest lines: `ctx_size`
  (1–10⁹) with `ctx_pct` (0–100) and optional `ctx_used` (0–10⁹), or
  `ctx_used` alone;
- `query` lines with a model, an effort and the `session_id` they were read
  for, never subagent evidence. Every current writer labels its query; the
  reader still accepts an unlabelled one (a writer on an image not yet
  recreated), which the fold never applies
  ([agent reports](agent-reports.md)), so the box's health stays valid.
  `SubagentStart` is subagent evidence ([Codex reporter](codex-observation.md));
- an enumerated `state` on the events [agent state](agent-state.md) lists.

`ReadWithin` checks a context between steps: a cooperative deadline that a
step in progress can overrun once, and that leaves a hung kernel call
unbounded. The cursor
(generation, k, offset, SHA-256 of the last line) makes rereads idempotent. A partial last line waits for the next read. A rotated-away
cursor, a missing index or a rewritten line is a `gap`. Health (`ok`,
`no-report`, `gap`, `invalid`) is reported beside observations; it never
decides an outcome. [3](#source-3) [11](#source-11)

[FACT] Unprivileged regressions fake the mount and mountinfo. They cover root
adoption and refusal, no-follow removal, private, reused and pruned
generations against planted links, FIFOs and deep trees, exhaustion recovery,
rotation, gaps, rewrites, tampered entries and lines, other generations, the
writer's bounds, status line fields and the Claude 2.1.311 vectors, interpreter
resolution and the bind lifecycle. The real mount, a VM reboot and a live
Claude run need the isolated live check. [12](#source-12) [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [Root path](../../internal/layout/observe.go#L1-L24) — `AgentObserveParent`, `AgentObserveDir`.
2. <a id="source-2"></a> [Contract constants](../../internal/agentsession/observe/observe.go#L1-L61) — `ContainerDir`, `EnvGeneration`, byte/inode caps, `ErrUnavailable`.
3. <a id="source-3"></a> [Root mount](../../internal/agentsession/observe/root.go#L19-L137) — `rootOptions`, `EnsureRoot`, `RemoveRoot`, `openRoot`, `mounted`; the Linux mount and filesystem check in root_linux.go, host stubs in root_other.go.
4. <a id="source-4"></a> [Box binding](../../internal/agentbox/observe.go#L1-L90) — `addObservationRunOptions`, `ObservationBound`, `PrepareObservationRuntime`, `RemoveObservationRuntime`.
5. <a id="source-5"></a> [Create and inspect](../../internal/agentbox/live.go#L125-L128) — `ObservationEnabled` classification; `Create` calls `addObservationRunOptions` after chat options; `ActualState.ObservationEnabled` in diff.go.
6. <a id="source-6"></a> [Start and destroy](../../cmd/yoloctl/agentbox.go#L1947-L1951) — `startExistingAgentboxContainer` remounts; destroy calls `RemoveObservationRuntime` after the container's VM state.
7. <a id="source-7"></a> [Generations](../../internal/agentsession/observe/generation.go#L1-L108) — `Prepare` (discardable evidence, proof-gated retry), `parseGenName`, `createGeneration`; `removeAt` in root.go.
8. <a id="source-8"></a> [Feature](../../internal/features/agent_observe.go#L1-L174) — status line, async hooks, interpreter resolution, wrappers and drop-in.
9. <a id="source-9"></a> [Writer](../../internal/features/agent-observe.py#L1-L829) — `record`, `statusline_record`, `codex_record`, `lock`, `append`, `write`, `main`.
10. <a id="source-10"></a> [Catalog entry](../../internal/features/features.go#L88-L92) — `yolo.agent.observe`.
11. <a id="source-11"></a> [Reader](../../internal/agentsession/observe/read.go#L1-L514) — `Health`, `Context`, `StatusLine`, `Query`, the states, `Cursor`, `Observation`, `Read`, `listEvents`, `wireEvent`, `parseLine`.
12. <a id="source-12"></a> [Substrate regressions](../../internal/agentsession/observe/observe_test.go#L1-L839) — root, generations, exhaustion recovery, reader, status line and query validation, the Claude and Codex vectors.
13. <a id="source-13"></a> [Writer regressions](../../scripts/test_agent_observe.py#L1-L843) — fields, payload shapes, absence, links, contention, rotation, full tmpfs, status line and hand-over, Codex mode and its daemon read; bounds and interpreter resolution in internal/features/agent_observe_test.go.
14. <a id="source-14"></a> [Binding regressions](../../internal/agentbox/observe_test.go#L1-L128) — bind, overlap, runtime remount, destroy, no recreate reason.
