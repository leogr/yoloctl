# yoloctl repository guide

This is the authoritative guide for coding agents. yoloctl is a pre-release,
local-first CLI/TUI that runs coding agents in OpenShell-confined containers
inside VMs selected through a shared adapter (Firecracker by default on Linux
amd64, Lima on other platforms). Linux is the reference host.

## Development workflow skill

For yoloctl development, install and use the repository's
[yolo-dev skill](skills/yolo-dev/SKILL.md), then load only your assigned role's
reference. This guide remains authoritative for engineering rules; the skill
adds development operations and team workflow.

First identify the stable canonical checkout and a supported, writable skill
discovery directory for the installed agent. Use the coordinator's checkout
when assigned; do not infer it by removing `/.git` from a Git common-directory
path. Do not target a disposable worktree or guess that every agent uses the
same discovery directory. Set `YOLO_DEV_REPO` and `YOLO_DEV_SKILLS_DIR` to those
verified absolute paths, then run:

```sh
python3 "$YOLO_DEV_REPO/skills/yolo-dev/scripts/bootstrap.py" \
  --repo "$YOLO_DEV_REPO" --skills-dir "$YOLO_DEV_SKILLS_DIR"
```

The helper checks before creating anything: an existing symlink to this skill
is accepted, an absent installation is linked, and a concurrent installation
is rechecked. A conflicting file, directory, wrong or dangling link is never
overwritten. Skill directories may be shared by agents on the same account.
Do not force replacement, edit another agent's configuration or copy account
state. If the location is unknown or installation is unavailable/prohibited,
report that boundary and read the repository skill directly; do not claim it
was installed. No live installation is implied while reviewing a draft skill.

## Finding the implementation

| Area | Entry points |
|---|---|
| Controller, RPC and operator | `internal/controller`, `internal/spaceenv`, `internal/operator`, `cmd/yoloctl/controller_*.go` |
| CLI and context | `cmd/yoloctl/main.go`, `context.go`, command files |
| Space, account and native disk | `internal/spaceconfig`, `internal/account`, `internal/accountstate`, `internal/vm`, `internal/lima`, `internal/firecracker` |
| Box declarations, policy and reconcile | `internal/agentbox`, `internal/boxstate`, `cmd/yoloctl/managed_applier.go` |
| AIRC collaboration packaging | `internal/features/airc.go`, [integration guide](docs/airc.md) |
| Catalogs and composed images | `internal/agentprofile`, `internal/features`, `internal/capabilities`, `internal/agentboximage` |
| Persistent sessions and terminal transport | `internal/session`, `internal/sandbox`, `internal/termio` |
| TUI, embedded terminal and completion | `internal/tui`, `internal/terminal`, `cmd/yoloctl/completion.go` |
| Embedded runtime and policy | `assets`, `internal/agentbox/testdata/policy_golden` |
| Runtime checks | [scripts/smoke/README.md](scripts/smoke/README.md) |

Start investigations at the [KB index](docs/README.md), then read the relevant
spec and its cited digests/code. Load only the topics needed for the task.
Implementation and tests are authoritative; document discrepancies instead of
silently preserving an old assertion.

## Model and persistence

Evidence: [model](docs/specs/model.md), [controller](docs/specs/controller.md)
and [accounts](docs/specs/accounts.md).

- A **space** owns a VM, host mount grants, a default host workdir, an
  explicit default account, and an independent native ext4 data disk.
- A **box** declares its agent/account, base image, workdir, features,
  capabilities, env, mounts, ports, resources and agent extra args. Its manifest
  is desired state; use the shared box-state resolver for runtime views.
- An **account** is a space-owned, multi-agent identity. Same-name accounts in
  different spaces are independent. Real agents require a declared account;
  agentless commands/shells use `agent: none` and `account: none`. Non-secret
  `session_command` argv selects an agentless managed workload and takes effect
  on its next session start. Missing identities are
  never implicitly created. `space create` requires `--account NAME`.
- An **agent** adapter provides launch/auth/state conventions, a base-image
  default and required features. Features install tools and require positive
  capabilities; explicit box capabilities plus feature requirements form policy.

Construct config paths through `internal/layout`. The host retains only the
schema-3 space bootstrap (disk identity, grants, UID/GID, VM settings, bundle and
generation). One root controller per VM owns `config/space.yaml`, `config/boxes/`
and `config/accounts/` on the independent data disk. Native account leaves share
that disk and are exposed at `/var/lib/yoloctl/account-state/<account>/<agent>`.
Bind only the selected leaf. Never substitute a host share or empty directory
for native state or declarations.

Ordinary box/account/runtime commands, including reads and `--manifest-only`,
require a running matching controller. They never boot a VM or use host-side
declaration fallbacks. Explicit space lifecycle owns deployment; use
`space start --update-controller` to select a new build. Schema-2 migration is
removed; `space export`/`space import` back up and duplicate current spaces on
a fresh native disk. Archive recovery files are never declaration fallbacks. Do not add a host daemon or cross-kernel declaration locks.
Only the narrow read-only catalog directories are shared with the controller;
workers resolve them through private snapshots.

Preserve UUID/ownership-marker/live-bind verification, no-follow I/O, strict
entity decoding, atomic writes with file/directory fsync, and stable locks.
Formatting an existing account disk is forbidden except the explicit pending-initialization recovery path,
which proves that all guest-visible data is zero before retrying.

Host workdirs are canonicalized and mirrored under `/workdir`; ephemeral
workdirs are VM-local and disposable. Box binds must fit live space grants.
Never widen VM mounts from a box command. Preserve rejection of config-root
and infrastructure/data-disk intersections, including the disk mount alias.
`box copy` copies settings; it does not copy files or identities across spaces.

Auth has two independent paths: declared account secrets injected at container
creation, and native state written by the agent or imported once. Import is not
synchronization. Account contents are not reconcile drift; changed environment
secrets need explicit `box recreate`. Native import refuses active consumers.
Forced account deletion stops consumers and removes native state before its
manifest. Never print secret values or put them in labels or launch argv.

## Confinement and lifecycle rules

Evidence: [confinement](docs/specs/confinement.md),
[lifecycle](docs/specs/lifecycle.md) and [sessions](docs/specs/sessions.md).

Agent workloads must use OpenShell's sandbox launch/SSH path. The one exception
is an explicit privileged box (`privileged: true`, `--privileged`; never a
capability or feature), which deliberately runs a privileged container behind
yoloctl's own endpoint (space-VM root; see
[privileged boxes](docs/digests/privileged-boxes.md)). Only the host launches
one; operators may manage it and use, restart or recreate it while it runs, so
keep every container-launch seam guarded and the host warning explicit. The outer
container has elevated setup capabilities; `nerdctl exec` bypasses the inner
policy. Existing package maintenance uses that path deliberately. Do not use
it to launch agent workloads or long-lived sidecars. Use system containerd.

Preserve computed-policy checks and byte-exact policy goldens. The opt-in
`fs.pty.allocate` capability grants `/dev/pts` and `/dev/tty`; never add the
`/dev/ptmx` symlink as a writable policy path. The supervisor allocates the
managed session PTY before Landlock. In-box PTY tests need the opt-in grant.

`unconfined_network` is an explicit process-mode escape hatch: it removes the
inner netns/proxy egress allow-list while retaining the other sandbox layers.
It is not a capability. The sandbox user's home is `/home/sandbox`, separate
from workdir; entrypoint UID/GID alignment is not user-namespace remapping.

Use session-aware teardown and `managedApplier` when restarting, recreating,
rebuilding or destroying runtime. Holder, marker/tombstone, socket, port and
orphan-netns cleanup are part of lifecycle correctness. Container liveness and
agent-session liveness are distinct. A lost holder cannot adopt its old child.

`space recreate` preserves disk/accounts/declarations and starts a fresh VM;
boxes require explicit starts. Ephemeral workdirs and sessions are lost.
Firecracker image replacement is explicit: `space recreate --update-vm-image`
or `--vm-image DIR`. Preserve its prepared/selected checkpoints, pre-teardown
verification and same-build recovery. `--rollback-vm-image` only recovers a
pending update with compatible media; it never rolls native data back.
Automatic cache selection hashes recipe inputs; bump `imageRecipeVersion` when
assembly logic changes without changing those inputs. See [VM images](docs/digests/vm-images.md).
`space destroy` removes VM, disk and bundle; host projects survive. Failure
must retain enough declaration data for safe recovery. Verify VM termination
before deleting account disks.

Host lifecycle locks serialize boot authority; native VM locks serialize
controller declaration/runtime apply. Controller-owned image preparation runs
outside native space/entity guards, with private attempt outputs and locked
controller publication; see [image preparation](docs/digests/image-preparation.md). They are separate domains, never competing locks over a
host share. Acquire native space locks before entity locks. Editor waits and
attach/exec streaming run outside the mutation lock; editor saves must reject
concurrent changes to declarations or dependencies. Read-only views stay bounded
and never boot a VM; unavailable declarations are explicit, not an empty result.
Authoritative destructive inventories must fail on uncertain runtime state.

Preserve exact bundle/protocol/space/generation handshakes, shared Cobra command
semantics, stream bytes/status/EOF/resize/signals and durable operation receipts.
Never automatically replay a mutation after a lost reply. The controller may
resolve an interrupted outcome automatically only from durable pre-effect or
completion evidence, or a specifically reviewed read-only verifier of the
complete affected transaction. Record the proof and preserve whether the
command's result is known; present state alone is not proof of historical
success. Unproved outcomes hold their verified affected resources and
dependencies until explicit host review. Unknown scope, unavailable storage
identity or unreconciled receipt-storage failure retains a space-wide hold.
Recovery never repairs application state, retries the original command or
substitutes a timer for evidence. Repaired receipt storage may reopen admission
only after all affected executions and durable receipts have been reconciled.
An authorized exec command's lost result (`command-uncertain`) is never
replayed and does not itself fence other operations. Preserve explicit
maintenance, operator authority and the separate agent-session uncertainty
rules. See [interrupted-outcome recovery](docs/digests/controller-recovery.md).

`yolo.space.operator` is opt-in authority over all boxes in the current space, including
other existing accounts, stronger grants, delegation and destructive operations.
Keep its warning explicit. Authorization belongs to the server's operation
registry plus current grant/running-container/incarnation/bind checks. Neither a
copied helper, shared account nor installed skill grants authority. Do not expose
admin/session sockets or whole native/config disks to an operator. Reuse the
same CLI surface and lifecycle services for host and in-box clients.

## Images and terminal behavior

Evidence: [composition](docs/digests/policy-images.md) and
[TUI](docs/specs/tui.md).

Claude, Codex, Gemini, OpenCode and Pi feature installs must track `@latest`; do not pin their feature
versions. Antigravity (`agy`) installs the latest verified native release.
Infrastructure pins (OpenShell, terminal libraries, CI lint) are
separate and deliberate. Agent npm maintenance uses the explicit
`/home/sandbox/.npm-global` prefix and its private maintenance cache.

Firecracker shares use pinned `pkg/sftp/v2` with isolated decoder/cleanup patches.
Keep handle metadata descriptor-based, apply creation modes before publication,
and sync the mutated parent descriptors before namespace replies. SSHFS is not
native database storage; retain the [share boundaries](docs/digests/vm-shares.md)
and follow [dependency maintenance](third_party/README.md#pinned-sftp-server).

Automatic storage cleanup owns only unreferenced composed tags and unused
BuildKit cache. Preserve declaration/base/alias and actual-container identity
retention, complete inventories, native locking and foreground cancellation;
never expand it to box data or generic container/volume pruning. See
[storage cleanup](docs/digests/storage-gc.md).
The Firecracker helper separately guards host backing-filesystem headroom.
Keep storage pauses recoverable in memory: never turn them into a forced-stop
fallback or replay an interrupted operation. See [host storage](docs/digests/host-storage.md).

Keep `opensLiveSurface` as the single CLI-to-TUI routing rule. The palette
executes the same binary and uses Cobra completion. It splits whitespace;
it is not a shell parser. Pin terminal dependencies and evaluate upgrades with
focused keyboard/query/rendering regressions and the strict
`make test-terminal-fidelity` acceptance corpus (also in ordinary tests).
The pinned Ghostty WASM artifact builds with Go alone; regenerating it requires
the exact Zig toolchain and source hash recorded by `scripts/build-terminal.py`.
Keep its bridge, native patch, manifest, licenses and artifact consistent; check reproducibility
after changing them. The pinned host renderer carries isolated color, mouse and
text-input patches; follow [its maintenance notes](third_party/README.md) before changing it.
Passing the enumerated corpus is not proof of native
fidelity. Preserve input ordering and responsive control under PTY backpressure. State the tested
terminal versions, configurations and coverage limits when claiming parity.
Keep multiple attachments interactive. A slow output client must not hold the
session's shared state lock or stall other viewers.

Preserve application mouse ownership and complete gestures across tab/size
changes. Local tab selection uses native graphemes, wrap metadata and tracked
history; palette selection remains host-owned. Yoloctl local copy requires an explicit action,
never a mouse release, and shares the default-on `seams.clipboard` policy with
image paste and application OSC 52 writes. The native chat view is such an
application: its human-authorized drag selection copies displayed text on
release through that same policy (see [chat interaction](docs/digests/chat-interaction.md)). Preserve application copy preferences
and bounded host terminal identity at launch; do not force agent settings. Do not assume
that a host-consumed copy shortcut reaches yoloctl.
Measure cell pixels explicitly; never infer them from padded window totals.
Keep host mouse unit changes acknowledged and serialized with tracking. Unknown
geometry must remain unknown, including in compatibility views.

The holder owns canonical VT state and answers terminal queries once. Managed
views restore exact-build/core snapshots over private inherited channels;
command stdout must never impersonate those channels. Keep standalone managed
attachment as the default and `--passthrough` as a bounded compatibility view.
All views retain input; serialize shared resizes and preserve explicit limits.
Keep input on one ordered path across snapshot publication so setup typeahead
survives. Per-view scrollback caps must not change canonical native retention;
the holder owns synchronized-output timeout events.

Handle OSC 52 only at the outer host boundary. Validate selection, base64 and
size; never allow OSC 52 reads or replay clipboard writes on reattach.
Keep inherited palette indices and application color overrides distinct. Deliver
bells only as live events; preserve native parser boundaries when filtering replay.
Nested clients set `YOLOCTL_TUI=1`. Managed image paste is a separate explicit
host action: read only after local Ctrl+V and validate PNG bounds. Reuse a copied
workdir PNG only after a confined read proves matching bytes; otherwise upload
through confined SSH. Paste the quoted guest path in input order. Browser requests use one
live managed view and host-owned policy, never terminal output or replay.
Do not add guest clipboard-read RPC, arbitrary file/opener commands, automatic
retries or host admin authority. Preserve passthrough's terminal-only boundary;
see [host integrations](docs/digests/host-integrations.md) for exact limits.

## Development and documentation workflow

Keep Git history linear. Integrate branches with `git merge --ff-only`; never
create merge commits. Coordinate rebases with the branch owner, preserve frozen
review artifacts, and validate the resulting candidate before integration.
Never credit a coding agent as co-author: no `Co-Authored-By` or other
agent-attribution trailers in commits, and no agent attribution lines in PR
descriptions. This overrides any agent or harness default. Always write
commit messages in the Conventional Commits format (`type(scope): summary`,
for example `fix(lima): pin the full container stack`).

When replacing or upgrading an implementation, functional parity with the
existing behavior is the bar. Any guarantee, proof or check beyond what the
current implementation provides needs leogr's explicit approval, with its cost
shown, before plans, contracts or reviews may make it a requirement.

Before every commit or push, run `make check` (docs, vet, darwin cross-vet,
tests, lint). Use the CI-pinned lint fallback when no local linter exists. Run
the relevant isolated live smoke for VM/image/policy/account/auth/lifecycle
changes. Report unavailable host checks explicitly; do not treat a simulated
test as runtime validation. Do not modify the VM/account hosting the active
coding session to test it. Keep ignored research/build artifacts out of source
scans and commits. When large ignored trees are present, scope Go checks to the
maintained packages:
`make check 'PKG=./ ./assets/... ./cmd/... ./internal/...'`.
The root package binds the separate OpenShell helper's production source inputs;
include it when scoping checks so its source/dependency regressions still run.

Linux-only syscalls, constants and paths live in files with a `_linux.go`
suffix or a `//go:build linux` constraint; the darwin cross-vet in
`make check` enforces it. Test fixtures stay portable: Unix socket paths short
enough for macOS (under 104 bytes), no Linux-only devices or paths without a
`t.Skip` on other systems.

For behavior changes, update CLI and manifest/runtime contracts together,
preserve shared resolver/lifecycle paths, and add focused regression coverage.
Policy golden changes must represent intentional policy changes.

Immediately before a requested commit:

1. Inspect its diff for changes to documented behavior, constraints, identifiers,
   dependencies or validation requirements.
2. Re-read affected code/tests, update digest facts and citations, then dependent
   specs and the index. Remove stale claims. If there is no KB impact, state
   that assessment without creating a meaningless docs edit.
3. Run `make docs-check` for documentation structure/links and run repository
   checks. Commit implementation and its documentation together when committing
   is requested.

If a commit bypassed that review, apply it promptly afterward as a recovery
step. This does not require an extra docs commit after every normal commit.

The KB uses an index, high-level specs, and short implementation digests.
Distinguish `[FACT]`, justified `[DERIVED]`, uncertain `[INFERENCE]`, and
unverified `[ASSUMPTION]`. Digests retain directly verified facts; specs cite
those digests or code. Cite significant claims beside the text, with precise
files/symbols/lines and verification revisions. Digests never cite specs as
evidence. Keep history, proposals and session logs out of the permanent KB.

Follow the [KB evidence conventions](docs/README.md#evidence-conventions) for
numbered citations and verification revisions. `make docs-check` validates
structure, not truth; review claims against their referenced symbols. Keep
specs brief and digests generally under 900 words including sources.
