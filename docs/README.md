# yoloctl knowledge base

Start with the relevant spec, then follow its numbered citations to a digest
or source. Code and tests establish current behavior; this KB records contracts,
implemented constraints and investigation boundaries. It contains no roadmap
or session history. Repository working rules are in [AGENTS.md](../AGENTS.md).

## Investigation map

| Question | Entry spec | Implementation evidence |
|---|---|---|
| How can the host enable Falco on Lima/Firecracker, inspect health/events and export an interval? | [CLI](specs/cli.md), [Controller](specs/controller.md) | [VM-native auditing](digests/audit.md), [event output/mandatory fields and folded presentation](digests/audit-output.md), [metadata extraction](digests/audit-metadata.md), [rule paths/escape candidates/container recognition](digests/audit-rules.md), [selection framing and validator qualification](digests/audit-rule-validation.md), [native rule management](digests/audit-rule-management.md) |
| How are releases installed and verified, which home does each build use, what does install advise for spaces, and how does it set up PATH and completion? | [Model](specs/model.md), [CLI](specs/cli.md) | [Build identity and installation](digests/installation.md), [install shell setup](digests/install-shell-setup.md); [installation guide](install.md) |
| Where do commands execute, and how are compatibility/recovery/operator authority enforced? | [Controller and storage diagram](specs/controller.md) | [Controller](digests/controller.md), [operator](digests/operator.md) |
| When does a crashed Firecracker controller restart, when does it stay failed, and how is that policy deployed? | [Controller](specs/controller.md), [Lifecycle](specs/lifecycle.md) | [Controller supervision](digests/controller-supervision.md) |
| How does the controller recover an interrupted operation or failed receipt storage by itself, and what stays held for host review? | [Controller](specs/controller.md) | [Interrupted-outcome recovery](digests/controller-recovery.md), [controller](digests/controller.md) |
| Why does an interrupted `box attach`/`box use` client not pause the space, and when may a session start after its client left? | [Controller](specs/controller.md), [Sessions](specs/sessions.md) | [Owned attach setup](digests/attach-setup.md) |
| Who owns declarations, identities and mounts; which VM backend and host prerequisites apply? | [Model](specs/model.md) | [Declarations](digests/declarations.md), [VM adapters](digests/vm-adapters.md), [VM images](digests/vm-images.md), [host shares](digests/vm-shares.md) |
| Why are new space names capped at 16 characters, and how do new and legacy macOS Lima homes coexist? | [Model](specs/model.md), [CLI](specs/cli.md) | [Lima storage and space names](digests/lima-storage.md) |
| Where are credentials stored, and what survives replacement? | [Accounts](specs/accounts.md) | [Native storage](digests/account-storage.md), [auth](digests/auth.md), [Claude profile storage](digests/claude-auth.md) |
| How do I provide AWS credentials, install the AWS CLI and grant Bedrock egress? | [Accounts](specs/accounts.md), [CLI](specs/cli.md) | [AWS CLI and Bedrock setup](digests/bedrock.md) |
| Why does an operation recreate, block, or fail to clean up; how are runtime targets selected; what updates the VM image? | [Lifecycle](specs/lifecycle.md) | [Reconcile, teardown and runtime targets](digests/lifecycle.md), [space archives](digests/space-archives.md), [VM image replacement](digests/vm-images.md) |
| What survives export/import, and why can a native link be rejected? | [Lifecycle](specs/lifecycle.md) | [Space archives](digests/space-archives.md) |
| How do I follow a granted VM file or explicitly watch a host file, including Markdown and large logs? | [CLI](specs/cli.md), [TUI](specs/tui.md) | [Live file view](digests/follow-view.md) |
| How do image builds share work, stream logs to every requester, publish immutable results and release cancelled attempts? | [Controller](specs/controller.md), [Lifecycle](specs/lifecycle.md) | [Image preparation](digests/image-preparation.md), [build progress](digests/image-progress.md) |
| How are unused images/cache collected, and why can cleanup or a low-disk build stop? | [Lifecycle](specs/lifecycle.md) | [Storage cleanup](digests/storage-gc.md) |
| Why can a Firecracker VM pause while its guest disk still has space? | [Lifecycle](specs/lifecycle.md) | [Host backing storage](digests/host-storage.md) |
| Why can a box not be attached after a controller update, and how are its sessions restarted or recreated? | [Lifecycle](specs/lifecycle.md) | [Sessions after a controller update](digests/controller-update-sessions.md), [remedies for sessions of another build](digests/session-remedies.md) |
| How does Firecracker forward ports, and which images preserve TCP half-close replies? | [Model](specs/model.md) | [Port forwarding](digests/vm-ports.md) |
| How are VM memory and CPUs chosen, and how can a stopped space's RAM/CPU settings change? | [Model](specs/model.md), [CLI](specs/cli.md) | [VM resource settings](digests/resources.md), [automatic VM CPUs](digests/vm-cpus.md) |
| How big is a space's VM system disk, what does formatting it cost, and how does it grow? | [Model](specs/model.md), [CLI](specs/cli.md) | [VM system disk size](digests/vm-disk-size.md) |
| How big is the data disk, and how does it grow without risking accounts and declarations? | [Accounts](specs/accounts.md), [CLI](specs/cli.md) | [Data disk size and offline grow](digests/data-disk-grow.md) |
| Which image and provisioned dependencies does Lima use, and how is the container stack pinned/upgraded? | [Model](specs/model.md) | [Declarations and Lima provisioning](digests/declarations.md), [Lima stack and upgrades](digests/lima-stack.md) |
| Which CA roots and verifiers do VM registry clients, box images and AIRC use, and how do updates reach existing workloads? | [Lifecycle](specs/lifecycle.md), [model](specs/model.md) | [CA trust stores](digests/trust-stores.md) |
| How do boxes and builds share RAM, and where are OOM/peak counters shown? | [Model](specs/model.md) | [Workload memory](digests/workload-memory.md), [space view memory](digests/space-view-memory.md) |
| What grants access, installs tools, caches package downloads, or updates an agent? | [Confinement](specs/confinement.md) | [Policy and images](digests/policy-images.md), [npm environment](digests/npm.md), [pip environment](digests/pip.md), [AgentMon Live feature](digests/agentmon.md), [Go environment](digests/go.md), [Gemini adapter](digests/gemini.md), [Antigravity adapter](digests/agy.md), [Pi adapter](digests/pi.md), [OpenCode and npm plugins](digests/opencode.md) |
| What changes for boxes in the OpenShell 0.1.6 upgrade, and which limits and known issues apply? | [Confinement](specs/confinement.md) | [OpenShell compatibility](digests/openshell-compatibility.md), [upgrade release notes](releases/openshell-0.1.6.md) |
| What does `box create --operator` enable, and who can operate other boxes? | [CLI](specs/cli.md), [confinement](specs/confinement.md) | [Operator preset and authority](digests/operator.md) |
| What does a privileged box (`privileged: true`) grant, how is its endpoint launched and verified, and who may start or use it? | [Confinement](specs/confinement.md), [controller](specs/controller.md) | [Privileged development boxes](digests/privileged-boxes.md) |
| What does `box show` report about Landlock, and what does that prove? | [Confinement](specs/confinement.md) | [Landlock status](digests/landlock-status.md) |
| How do OCI Features, options, pinned artifacts and autocomplete work? | [Confinement](specs/confinement.md) | [OCI Features](digests/oci-features.md) |
| Which box names and space/state/output contracts are accepted? | [CLI](specs/cli.md) | [CLI and resolver](digests/cli-state.md) |
| What does `space view` show, where do its live numbers and chat links come from, and what does a click do? | [CLI](specs/cli.md), [TUI](specs/tui.md) | [Live space view](digests/space-view.md), [observed box activity](digests/box-activity.md) |
| What fills the VM's system disk, when does space view warn about either disk, and what does `space storage` measure? | [TUI](specs/tui.md), [Lifecycle](specs/lifecycle.md) | [VM disks](digests/space-view-disks.md), [storage cleanup](digests/storage-gc.md) |
| What do space view's VM, pool and box memory numbers measure, what is the working-set estimate, and when is a reading current? | [TUI](specs/tui.md) | [Space view memory readings](digests/space-view-memory.md), [workload memory](digests/workload-memory.md) |
| How do chat participants show explicit IRC availability and box activity, and where does the space-view chat row come from? | [TUI](specs/tui.md) | [Managed chat presence](digests/chat-presence.md), [observed box activity](digests/box-activity.md) |
| How are host/guest builds matched, and what persists after detach or exit? | [Sessions](specs/sessions.md) | [Holder installation](digests/sessions.md), [canonical state and compatibility replay](digests/session-replay.md), [attachment frontends](digests/session-views.md), [native snapshot API](digests/terminal-snapshots.md) |
| How does a box keep and resume its own agent conversation, and when is a launch uncertain? | [Sessions](specs/sessions.md) | [Agent conversation identity](digests/agent-conversations.md), [tracked launches and settlement](digests/agent-launches.md) |
| Why does a Codex app-server daemon not survive into the next launch, and which processes does the holder stop? | [Sessions](specs/sessions.md) | [Agent daemon lifetime](digests/agent-daemons.md) |
| Which conversation does a box resume after an in-app switch, when no saved file exists, or after a failed resume? | [Sessions](specs/sessions.md) | [Following agent conversation switches](digests/conversation-following.md), [saved conversation presence](digests/resume-file-presence.md) |
| How can agents observe and steer a session with finite `box use` requests or a persistent structured attachment? | [Sessions](specs/sessions.md), [CLI](specs/cli.md) | [Structured box-use frontend](digests/box-use.md) |
| How does an agent box report session, model, effort, context and attention state, and how do reports reach its conversation record? | [Sessions](specs/sessions.md) | [Agent observation substrate](digests/agent-observation.md), [Claude status line and hook reporter](digests/claude-reporter.md), [Codex hook reporter](digests/codex-observation.md), [Codex refresh read](digests/codex-refresh-read.md), [Antigravity status line reporter](digests/agy-observation.md), [agent reports in conversation tracking](digests/agent-reports.md), [agent state and used-only context](digests/agent-state.md) |
| How do agents share AIRC conversations, and why can a delivered batch wait for a Claude dialog to close? | [Model](specs/model.md), [Sessions](specs/sessions.md) | [AIRC packaging](digests/airc.md), [AIRC upstream contracts](digests/airc-upstream.md); [setup guide](airc.md) |
| How does managed chat own its room, apply membership and confine the human stream? | [Controller](specs/controller.md), [Model](specs/model.md) | [Native ownership and archives](digests/managed-chat.md), [runtime and stream](digests/chat-runtime.md) |
| Which managed chat operations can a space operator perform, and how is the human server protected? | [Controller](specs/controller.md), [CLI](specs/cli.md) | [Operator chat administration](digests/operator-chat.md) |
| How do chat roles, stopped holders, nickname settings and identity-guarded attachment work? | [CLI](specs/cli.md), [Controller](specs/controller.md) | [Roles, settings and attachment](digests/chat-settings.md) |
| How do managed channel identities, per-agent access, reloads, stream refresh and explicit deletion work? | [CLI](specs/cli.md), [Controller](specs/controller.md) | [Channel catalog and stream](digests/chat-channels.md), [Channel deletion](digests/chat-channel-deletion.md) |
| How is Markdown parsed, laid out and mapped for copy without a chat model? | [TUI](specs/tui.md) | [Shared Markdown renderer](digests/markdown-renderer.md) |
| How does native chat edit, match renderer widths, retain uncertain drafts and recover catalog refreshes? | [TUI](specs/tui.md) | [Chat UI component](digests/chat-ui.md), [width methods](digests/chat-width.md) |
| How does each TUI keep its own space, switch it, and handle open tabs? | [TUI](specs/tui.md), [CLI](specs/cli.md) | [Per-TUI space selection](digests/tui-space-switch.md) |
| How does a TUI tab split into panes, and which pane decides the cursor, title and keys? | [TUI](specs/tui.md) | [TUI panes](digests/tui-panes.md), [pane requests, attention and shared sessions](digests/tui-pane-requests.md), [pane chrome](digests/tui-pane-chrome.md), [pane prefix](digests/tui-pane-prefix.md), [pane menu](digests/tui-pane-menu.md), [empty panes, tab moves and docking](digests/tui-pane-moves.md), [close confirmation](digests/tui-close-confirm.md), [re-attach and view reopen](digests/tui-reattach.md) |
| How are host clipboard PNGs published with bounded input and uncertain-result recovery? | [Controller](specs/controller.md) | [Chat image publication](digests/chat-images.md) |
| Which panes may display chat image preview cells, and what happens to other image placeholders in the TUI? | [TUI](specs/tui.md) | [Chat image previews](digests/chat-image-previews.md) |
| How does the host read a chat image artifact for a preview, and how is the confined relay bounded? | [TUI](specs/tui.md) | [Chat image fetch](digests/chat-image-fetch.md) |
| How does a chat pane ask for, draw and release an image preview? | [TUI](specs/tui.md) | [Chat image panes](digests/chat-image-panes.md) |
| How do chat selection/copy, participants, suggestions, help/settings, attach and scrolling behave? | [TUI](specs/tui.md) | [Input and actions](digests/chat-interaction.md), [reading and selection](digests/chat-reading.md), [participants, settings and attachment](digests/chat-participants.md) |
| How does chat Quote turn a highlight into quoted original Markdown or table cell text, and when does it refuse? | [TUI](specs/tui.md) | [Chat Quote](digests/chat-quote.md) |
| How does the chat For me/participant filter choose messages and replies, and whom does an `@name` mean across live aliases? | [TUI](specs/tui.md) | [Message filter and mention identities](digests/chat-filter.md) |
| How does the chat Thread view pick a whole reply tree, and what happens when its start is not loaded? | [TUI](specs/tui.md) | [Chat thread view](digests/chat-thread-filter.md) |
| What model, effort, context and state do chat participants show, and where do the values come from? | [TUI](specs/tui.md) | [Participant model and effort](digests/chat-participant-runtime.md), [agent state](digests/agent-state.md) |
| How can optional AIRC delivery reach a busy Codex turn, and what prevents uncertain replay? | [Sessions](specs/sessions.md) | [AIRC packaging](digests/airc.md), [upstream delivery contracts](digests/airc-upstream.md) |
| How do shared `m:` and `msgid:` message references resolve, and why is an `@N` number plain text? | [TUI](specs/tui.md) | [Chat message references](digests/chat-references.md) |
| How are log lines rendered (levels, glyphs, colour, folding, steps), how do worker records reach the host, and what do `--log-level`/`-q`/`--color` change? | [CLI](specs/cli.md) | [Log presentation](digests/log-presentation.md), [TUI notices](digests/tui-log-notices.md), [CLI routing and outcomes](digests/cli-logging.md) |
| How does chat keep per-channel state and refresh desired identities without a runtime scan? | [TUI](specs/tui.md) | [Channels and live identities](digests/chat-channel-view.md) |
| How do chat hover highlights and collapsed `[Pasted…]` chips work? | [TUI](specs/tui.md) | [Hover and pastes](digests/chat-hover-paste.md) |
| How do inline `[Image #n]` drafts keep their order and send text and images safely? | [TUI](specs/tui.md) | [Composer image drafts](digests/chat-composer-images.md) |
| How does a draft with images become one message for people and agents? | [TUI](specs/tui.md) | [Text and images as one message](digests/chat-staged-images.md), [AIRC staged attachments](digests/airc-attachments.md) |
| Where do background VM/controller logs live, how do they rotate, and how does `space logs` display them? | [CLI](specs/cli.md) | [Background logs](digests/background-logs.md) |
| Why do Lima warnings stay off live views, and where are host diagnostics logged? | [TUI](specs/tui.md), [CLI](specs/cli.md) | [Host and Lima diagnostics](digests/host-diagnostics.md) |
| Why do input, display, titles, selection preferences, paste, console history or clipboard behave this way? | [TUI](specs/tui.md) | [Compatibility evidence](digests/terminal-compatibility.md), [input](digests/tui-input.md), [mouse](digests/terminal-mouse.md), [geometry and pixels](digests/terminal-geometry.md), [selection](digests/terminal-selection.md), [core and PTY](digests/terminal-rendering.md), [display](digests/terminal-display.md), [colors and bells](digests/terminal-colors-bells.md), [titles](digests/sessions.md), [clipboard](digests/clipboard.md) |
| How do explicit image paste and guest browser requests reach the host? | [TUI](specs/tui.md) | [Host integrations](digests/host-integrations.md), [clipboard boundary](digests/clipboard.md) |

## Evidence conventions

- `[FACT]`: directly verified implementation, test contract or attributed source.
  A test's existence does not mean a live environment passed it.
- `[DERIVED]`: a conclusion with stated factual premises and citations.
- `[INFERENCE]`: uncertain interpretation, explicitly limited; never silently
  promoted to a contract.
- `[ASSUMPTION]`: unverified premise needing investigation, not implementation
  authority.

Digests contain verified facts; specs connect them and may state justified
deductions. Keep uncertain leads separate from contracts. Significant claims
have adjacent numbered links to a `Sources` entry. Local source entries identify
the file, symbol and relevant line span; external evidence identifies the exact
version when available. A reporter's claim is evidence of the report, not proof
of an unaudited implementation.

Each digest records a verification commit. It identifies the source baseline,
not a history narrative. For a change being committed with docs, use the parent
revision plus `and accompanying changes`; after an outside commit, use that
commit. Never try to embed a commit's own hash in itself. Re-read the current
symbol whenever it changes; line anchors alone do not prove a claim.

## Updating the KB

Immediately before a requested commit, review the intended diff, re-read affected
code/tests, update digest facts/citations, then dependent specs and this index.
Commit code and its documentation together. When a diff has no KB impact, state
that assessment without a token documentation edit. If a commit bypassed this
review, perform it promptly afterward as recovery. The complete working process
is in [AGENTS.md](../AGENTS.md#development-and-documentation-workflow).

Keep specs brief and digests focused, generally below 900 words including
sources. Split by investigation responsibility; avoid schema/help duplication
and one page per source file. Evidence flows spec → digest/code and digest →
code/test/upstream (or another digest), never back to a spec or index.

`make docs-check` validates local links/anchors, index coverage, numbered source
structure and evidence direction, and tests the checker. It joins `make check`.
It does not verify factual truth, upstream availability, or runtime behavior.
For runtime checks, use the [smoke guide](../scripts/smoke/README.md).
