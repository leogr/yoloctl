# TUI and host terminal boundaries

[FACT] `opensLiveSurface` owns routing to tabs versus palette results. The
palette uses CLI subprocesses and completion, not an independent command API.
Its editor keeps one row by converting whitespace to spaces and dropping other
terminal controls; this normalization does not apply to live child input.
Keyboard encoding, mouse forwarding and bracketed paste depend on child mode
and focus. Live tabs reserve the palette chord and an explicit copy shortcut
when local text is selected.
[1](#source-1)

[FACT] Lima launcher diagnostics are captured in the private host log
`<config-root>/logs/host.log`, including successful warnings. Native chat
and telemetry callbacks retain private stderr captures and return failure
context to the view. Empty/absent Lima homes are skipped before listing;
foreground application streams retain their explicit destinations.
The log is bounded (1 MiB plus one previous generation), and a failure to
write it never fails an operation that succeeded.
[42](#source-42)

[FACT] Palette commands echo without PWD. A fixed-width ASCII wave replaces the
left brand while busy; bold `space ▾` is the rightmost status item. Each TUI pins its default
independently of global `space use`. The chip or pane-prefix `s` opens a local
space dropdown; switching asks before closing all tabs and clearing drafts.
Explicit command targets still win, and a busy palette command must finish
or be interrupted before switching. [44](#source-44)
[1](#source-1)

[FACT] A tab can be split into panes, each hosting one palette subprocess
as a tab does, with a 20×5 minimum and no fixed count. The focused pane of the
current tab is the single source of keys, title, cursor and focus reports;
other panes show no cursor. An undivided tab renders exactly as before.
[32](#source-32) An attach request reuses an identical pane, else fills its
tab's empty pane, else opens a new tab. One session in
two panes shares one size: the latest resize wins and the other pane shows a
short or clipped frame until it resizes. Unfocused panes with a bell, exit or
request are marked. [33](#source-33) The pane prefix (`Ctrl+Q`, configurable)
is the second key reserved inside a child: after it, single keys split, move
focus, resize, zoom, swap and detach panes and switch, pick, open or detach
tabs. It opens a menu at the focused pane's top-left listing them; a key or a
click on a row acts, a refusal stays in its footer for at least 3 s, and the
prefix pressed twice reaches the child. The tab picker and the close
confirmation open at their pane the same way. [36](#source-36) [38](#source-38) A split opens an empty pane at once, which a live command,
an attach from its tab or a whole tab fills; prefix `j` and a dock drag of a
tab label move a tab's whole layout into the current tab, prefix `!` breaks a
pane out, all-or-nothing against the 20×5 minimum. [37](#source-37) Every pane of a divided or zoomed tab has
a bar below it: a band, orange with `▸` on the focused pane and blue on the
others, with the title, its mark and the zoom and detach buttons. A bar above
another pane is the horizontal border. Vertical borders are blue, orange
beside the focused pane; where one crosses a bar row it stays off the band,
as a half block leaning toward the focused bar or a `│`.
Every tab label ends in ✕, which detaches the tab. Strip, bar and border presses
are chrome gestures: a tab activates on release, a button acts only on a release
over itself, and a border or separator-bar drag previews and resizes the
sessions once on release. [34](#source-34) Closing ends a pane's local client: an established
holder-backed view keeps its session running and closes at once, while any
other live command asks first in a popup bound to the commands it lists.
[35](#source-35)

[FACT] An ended `attach`/`box attach` view offers re-attach in place: `r`, or
a click on its `[ Re-attach ]` button, runs the same attach again (without
`--new-session`) in the same pane slot; Enter still closes. Nothing
re-attaches on its own. Lifecycle auto-attach panes also offer ordinary
`box attach` for their retained resolved target, including after a remote error;
this does not prove lifecycle success or replay its command. Suppressed and
noninteractive lifecycle commands are excluded. [41](#source-41)

[FACT] Ended chat, space-view, followed log, `follow` file-view and interactive VM/box-shell panes
use `[ Reopen ]`/`r` in the same slot. Arbitrary remote commands, shell scripts,
editors and secret/input prompts are excluded. Implicit spaces are pinned in
the initial child argv and reused unchanged; changing palette context cannot
redirect a reopened view. Running space view keeps its own telemetry reconnect.
[41](#source-41)

[FACT] A disconnected but still-running chat offers Reopen in its footer:
click, or Shift+Tab to focus then `r`/Enter; Tab/Esc returns to the draft.
It uses the existing explicit stream callback, retains draft/reply/history and
uncertain deliveries, never resends or automatically takes over a nickname,
and disables duplicate activation while reconnecting. Literal editor text,
paste and overlays keep priority. A failure remains visible and permits
explicit retry; telemetry-only loss does not request IRC reconnect.
[46](#source-46)

[FACT] Private attachment failures use the palette's wrapped result area,
deferred while a palette command runs. Background failures leave another live
tab's input focus intact. A stream that ended without an exit status (for
example, the holder of a stopped box) is shown in the status line instead,
without opening the palette over the ended view. [1](#source-1)

[FACT] `space view` opens in a tab unless help is requested. Only that tab
and native `chat` tabs may request new attach tabs through the private channel. Through it, the view can
ask for a `box attach NAME --space SPACE` tab with validated names; chat may
only ask for a participant attach guarded by its room, member and box
identities. The channel never carries a command line. Outside the TUI, either
view hands the terminal to `box attach` and resumes after detach. Eligible
lifecycle panes use the same framing only for retained-target metadata, never
an open-tab action. [41](#source-41)
[14](#source-14) [39](#source-39)

[FACT] Space view shows the system disk (boxes, images, build caches) beside
the data disk (accounts and box settings, kept across VM rebuilds). Both use
one rule, warn under min(4 GiB, 15%) free or from 85% used and critical under
min(2 GiB, 10%) or from 95%, shown in the band, the title's warning slot and one toast per worse
crossing. Unknown usage is never shown as a healthy gauge. The system disk
popup runs `space storage --json` on demand. [28](#source-28)

[FACT] Space view's VM memory fill uses only its labeled
`MemTotal − MemAvailable`. Box and pool charges (`memory.current`, file cache
included) are a different measure and need not add up to it. Box cards add a
labeled working-set estimate, `current − inactive_file`, as a display
convention rather than non-reclaimable memory, and name the cap, pool or VM
scale of their bar. Memory comes only from a current sample; a stopped,
failed or stale reading shows its reason instead. [27](#source-27)

[FACT] Clipped Markdown table rows start compact: ▸ expands, ▾ closes; follow
also offers `w` for all rows. Copy includes hidden reached cells; within-cell
partial drags stay partial. Expansion limits leave a visible notice.
Wide Markdown tables use readable wrapped columns or labelled rows;
a counted limit notice replaces an unexplainable rendering failure. Existing
source/copy/Quote behavior and rendering limits remain. [43](#source-43)

[FACT] Markdown parsing, layout, copy geometry and optional source maps live in
`internal/markdownview`; chat retains identities, previews, model state and
effects. The extraction preserved the existing syntax and bounds; the subset
since adds code-span table pipes, heading levels, aligned ordered items, list
hanging indents, `---` rules and angle autolinks, without new blocks. [43](#source-43)

[FACT] `follow PATH` uses the shared renderer for a live file pane, with source
toggle, drag selection copied on release as in chat (plus explicit `y`) and inert links. It polls once per second,
refreshes off the input loop, follows the end until scrolling up and retains a
stale display on read failure. Page keys browse the active plain window for
large files; the footer identifies omitted bytes. [45](#source-45)

[FACT] The native chat component supplies multiline editing, bounded block and
inline Markdown display, source/code copy, a sorted Participants panel with
trusted identities, presence and topic notices, live mention and command
suggestions, `/help` and `/settings`, Ctrl+P for Participants, a shared `◈`
chat icon and a dashboard-style header, click-drag selection with copy on release
(a human-authorized exception, shared with the follow view, that still honors `seams.clipboard`),
a Quote action (Alt+Q or a footer chip) that puts the original Markdown of a
highlight inside one message into the draft as quoted lines and replies to it,
refusing rather than approximating a part it cannot map exactly, except that a
rendered table quotes its selected cell text with TAB between cells and one line
per row, without the clipboard [30](#source-30),
renderer-matched Unicode widths for layout and hits (wcwidth until a supported
mode-2027 report; grapheme widths thereafter),
older-history paging, per-channel views with typed join/part/topic actions,
channel navigation retained at narrow widths (a one-row strip when no side panel
is shown, outside the participant overlay) [17](#source-17),
explicit human channel selection that requests JOIN when needed (with drafts
kept and fresh catalog/server ACL admission), while background updates and
explicit part never automatically rejoin [18](#source-18),
live trusted-identity refresh from one desired controller catalog by default
(`chat --catalog-read=single`), with captured diagnostics
and acceptance-owned warning recovery [18](#source-18), receipt-aware
draft retention, a safe highlight/drag reset on a late width-method change
while preserving draft and reading position [40](#source-40),
hover highlights consistent with space view, long pastes
collapsed into chips that still send their full text, and Ctrl+V images as
inline `[Image #n]` placeholders sent in draft order as AIRC artifacts without retry, or,
when the room's AIRC stages images, staged first and sent with the text as one message
whose `[Image #k]` markers place them [22](#source-22). Participants show agent
boxes' observed model and effort and leave out unknown or stale values; the
details card adds the reported context window and, when certain, the running
launch's conversation (fresh, resumed, learned or switched) [23](#source-23).
The card's State row shows the agent's reported state, or the box's activity
when it reported none; the panel tags a name only for a wait or an API error
(`⏳ needs permission`, `⚠ API error`) [31](#source-31). Copy reference puts a message's shared `m:`
reference on the clipboard; a click on one, or on `msgid:` and the full ID,
shows that message when it is loaded in an open channel (switching to that channel
the way selecting it does), and an `@N`
number is plain text, never followed [24](#source-24). A reference to a message
loaded in the same channel shows in its place as `↗ author: text…` (from another
open channel `↗ #channel · author: text…`), still
copied and quoted as the reference [24](#source-24). Mentions resolve by account: suggestions insert a
trusted account's saved nick once across its live aliases, and the divider's
For me toggle beside the channel (Alt+M), the Alt+F picker, `/filter` or a card
shows only the loaded messages any selected account sent or that mention one,
and loaded replies to such a shown message, without changing messages, drafts
or paging
[25](#source-25). A selected message's Thread action or `/thread` instead shows
its whole loaded reply tree from the top-most loaded ancestor, replacing and
replaced by those filters, and says when the thread may start above the loaded
history without fetching it [29](#source-29). Its transport and
host effects are injected; queued acceptance never confirms delivery, and an
uncertain send is not retried. `chat` connects this component to a confined
structured AIRC client through the controller; finite chat subcommands remain in
the palette. Host browser policy also applies to chat links: `ask` presents the
fixed URL for a second Ctrl+O, Esc cancels, and modal input leaves the draft
untouched. Fixture tests establish component/adapter behavior, not final
host/VM integration. [15](#source-15) [16](#source-16) [17](#source-17) [18](#source-18) [19](#source-19) [20](#source-20) [39](#source-39) [40](#source-40)
[FACT] Catalog refresh and pre-JOIN reads use the same fixed mode. The channel
picker says server application was not checked; `--catalog-read=observed`
retains observed application diagnostics without automatic fallback. Catalogs
grant no action authority. [18](#source-18)

[FACT] Box title templates and agent icons are selected when the holder starts.
The holder initializes the template with an empty application title, so quiet
agents still identify their box; `{title}` alone opts out of initialization.
With `{agent-icon}` in the template, the holder removes a matching leading icon
before composition. The tab strip removes an exact leading `yoloctl — ` only
from its displayed labels. The window title collapses repeated exact leading
copies into one wrapper, so nested yoloctl views do not stack it; an empty or
name-only remainder shows `yoloctl`. [1](#source-1) The TUI colors only the active tab's leading icon; inactive
icons retain their tab's foreground and underline. [8](#source-8)

[FACT] The first row of each yoloctl-owned tab view is explicit RGB black
across the full width in every theme, with dark-palette text: chat's top band,
space view's title row, their resize hints and chat's browser-prompt border.
Space view's operator warning and privileged-box count there are colored
text, not red pills; its cards and compact rows mark a declared or evidenced
privileged box with a warning pill. The active
tab above keeps the terminal's default background. [14](#source-14)
[17](#source-17) [26](#source-26)

[DERIVED] The row therefore continues the active tab exactly on a black
terminal. Other surfaces `opensLiveSurface` routes to tabs draw guest or
external applications (attach, exec, SSH, editors) or are streams and prompts
(logs, secret input); none has a yoloctl header, so none is recolored.
[1](#source-1) [14](#source-14) [26](#source-26)

[FACT] A pinned libghostty-vt WebAssembly core owns parsing, screens,
scrollback, replies and modes. Its Go adapter serializes access; normal builds
do not need Zig or CGO. Typed input and
query replies use a bounded queue so PTY backpressure cannot block the model;
rejection and transport failures are visible. Terminal resets
retain final output, and process completion cannot wait forever for a grandchild
to close the PTY. [2](#source-2)

[FACT] `seams.clipboard` defaults on and controls yoloctl copy-out and explicit
image reads. Host-terminal copy/paste remains independent; native clipboard
reads inside a box are unsupported. [3](#source-3) [13](#source-13)

[FACT] OSC 52 writes are validated at the outer host boundary; OSC 52 reads
are denied and writes excluded from replay. Managed views separately handle
explicit host Ctrl+V: a copied workdir PNG is reused after confined content/access
verification; other PNGs are transferred. Its guest path is pasted in input
order. Ctrl+V accepts single file references; terminal text paste remains literal,
even for PNG paths. This does not emulate an agent's native image-attachment
API. [3](#source-3) [13](#source-13)

[FACT] A confined browser helper at a stable box-local path requests HTTP(S)
URLs through the live holder. Its path survives holder restarts so background
agents can retain it; interrupted requests fail without retry.
One accepting managed view receives each request;
the host opens automatically by default. Explicit `ask` opens a rounded
prompt on the palette background and restores the previous view afterward;
`off` disables registration. Requests expire and are
never replayed or rerouted after disconnect. No host daemon, administrative
authority or login callback tunnel is added. Passthrough and ordinary exec
remain outside both host integrations. [13](#source-13)

[DERIVED] A clipboard image failure must be investigated separately from key
encoding and copy-out. A guest application's clipboard reader cannot receive
host pixels solely because the terminal forwards Ctrl+V. Version-specific agent
clipboard mechanisms remain evidence about those versions, not stable yoloctl
extension APIs. [1](#source-1) [3](#source-3)

[FACT] The strict 35-scenario core corpus runs in ordinary tests, with five
write-fragmentation schedules per scenario. Keyboard tests cover negotiated
events and modifiers across host decoding and child encoding. Display frames
capture styled rows and cursor state together, honor synchronized output with
a bounded hold, and anchor history through new output/reflow. Cursor style and
requested focus reports reach the child/host boundary. Indexed colors inherit
the host theme; application overrides stay local. Live bells reach the host,
while replay neutralizes old bells. These boundaries have separate regression
coverage. [1](#source-1) [2](#source-2) [9](#source-9)
[5](#source-5)

[FACT] The same fidelity gate includes real host-reader/PTY cases for legacy
fallback and committed Unicode/IME text, plus focus ownership and cursor/cell
placement. Kitty text-only events preserve their payload. Graphical IME
composition and font-specific rendering are outside that automated evidence.
[12](#source-12)

[FACT] Mouse gestures keep their application/chrome owner through tab and size
changes. Native encoding covers cell-based protocols, including busy palette
commands and buttons through 11. Focused child Shift-capture preferences reach
the host, with gesture ownership and exit cleanup; forced host policy still
wins. Pixel reports require measured cells and acknowledged host support;
managed views scale positions to canonical geometry. Passthrough cannot provide
that geometry or scaling. Mouse-reporting applications own their selection.
Other tab content supports plain-drag native selection and explicit copy; palette
selection stays host-owned. Yoloctl local selection never copies on release. Applications retain their own
copy preferences. Host terminal name/version reaches new session launches so
agents can recognize terminal-specific preferences; existing processes keep
their launch environment.
[6](#source-6) [7](#source-7)

[FACT] Image placeholder cells (Kitty U+10EEEE) reach the display only inside
the one chat preview registered for that pane's session; any other placeholder
in any pane or the palette console is shown as a space. A native chat pane's
explicit copy turns preview cells into their attachment chip.
[7](#source-7) [47](#source-47)

[DERIVED] Passing these tests cannot establish native terminal equivalence.
Claims must name the tested terminal versions/configurations and distinguish
core scenarios from selection, frame rendering, reflow, replay and real agent
interaction. [1](#source-1) [2](#source-2) [5](#source-5)

Start with input/routing for misdirected keys or clicks, VT evidence for display
or query failures, session evidence for detach/replay, and clipboard evidence
for image/copy-out behavior. The holder owns canonical terminal/query state;
managed frontends restore snapshots while compatibility views use bounded
transcripts. [4](#source-4) Retain focused regressions
when changing each layer.

[FACT] Managed sessions use bounded, exact-build/core snapshots restored quietly
into isolated replicas. Standalone attach defaults to the same renderer without
chrome; `--passthrough` is an explicit compatibility option. Private inherited
channels separate native state from command output. These modes share query
ownership and remain interactive. The CLI owns process signals; frontend
cancellation restores terminal settings and reporting modes before returning.
After attachment ends, the completed view uses its own dimensions for the exit
banner and subsequent local resizing.
[10](#source-10) [11](#source-11)

[FACT] Chat participant availability is each participant's own IRC state:
connected, or an explicit AWAY with its reason. Listener settings, agent
sessions, box activity and telemetry freshness never change it. The
`space.telemetry` read, shared with the space-view chat row, maps box
participants to boxes from the verified server's connection counts. Their
observed box activity makes the dot breathe without changing its color,
through the same `ActiveTone` helper space view's RUNNING dots use. Humans
get no box evidence. A connection never promises that an agent wakes.
[14](#source-14) [21](#source-21)

[FACT] The isolated attach smoke combines all three frontends with changing
output, resizes, an unread viewer, detach/rejoin and final status. Its guest audit
checks transport behavior; it does not measure graphical parity. [4](#source-4)

## Sources

1. <a id="source-1"></a> [Routing, keys, mouse and paste evidence](../digests/tui-input.md).
2. <a id="source-2"></a> [VT and PTY lifecycle evidence](../digests/terminal-rendering.md).
3. <a id="source-3"></a> [Clipboard output and image-input evidence](../digests/clipboard.md).
4. <a id="source-4"></a> [Input replay and continuation](../digests/session-replay.md).
5. <a id="source-5"></a> [Display snapshots, history and focus](../digests/terminal-display.md).
6. <a id="source-6"></a> [Mouse delivery and selection boundaries](../digests/terminal-mouse.md).
7. <a id="source-7"></a> [Native selection and explicit copy](../digests/terminal-selection.md).
8. <a id="source-8"></a> [Session titles and icon configuration](../digests/sessions.md).
9. <a id="source-9"></a> [Host colors and live terminal bells](../digests/terminal-colors-bells.md).
10. <a id="source-10"></a> [Native terminal snapshots](../digests/terminal-snapshots.md).

11. <a id="source-11"></a> [Attachment frontends and private transport](../digests/session-views.md).
12. <a id="source-12"></a> [Compatibility evidence and limits](../digests/terminal-compatibility.md).
13. <a id="source-13"></a> [Explicit host image paste and browser requests](../digests/host-integrations.md).
14. <a id="source-14"></a> [Live space view and its tab requests](../digests/space-view.md).
15. <a id="source-15"></a> [Native chat component and validation boundaries](../digests/chat-ui.md).
16. <a id="source-16"></a> [Managed human stream](../digests/chat-runtime.md).
17. <a id="source-17"></a> [Native chat input and message actions](../digests/chat-interaction.md).
18. <a id="source-18"></a> [Native chat channels and live identities](../digests/chat-channel-view.md).
19. <a id="source-19"></a> [Native chat hover and collapsed pastes](../digests/chat-hover-paste.md).
20. <a id="source-20"></a> [Native chat composer image drafts](../digests/chat-composer-images.md).
21. <a id="source-21"></a> [Shared chat presence and activity](../digests/chat-presence.md).
22. <a id="source-22"></a> [Text and images as one message](../digests/chat-staged-images.md).
23. <a id="source-23"></a> [Participant model and effort](../digests/chat-participant-runtime.md).
24. <a id="source-24"></a> [Chat message references](../digests/chat-references.md).
25. <a id="source-25"></a> [Chat message filter and mention identities](../digests/chat-filter.md).
26. <a id="source-26"></a> [Tab strip](../../internal/tui/tui.go#L2391-L2439) — `tabStrip`: the active tab drops the surface background.
27. <a id="source-27"></a> [Space view memory readings](../digests/space-view-memory.md).
28. <a id="source-28"></a> [VM disks in space view and `space storage`](../digests/space-view-disks.md).
29. <a id="source-29"></a> [Chat thread view](../digests/chat-thread-filter.md).
30. <a id="source-30"></a> [Native chat Quote](../digests/chat-quote.md).
31. <a id="source-31"></a> [Agent state and used-only context](../digests/agent-state.md).
32. <a id="source-32"></a> [TUI panes](../digests/tui-panes.md).
33. <a id="source-33"></a> [TUI pane requests](../digests/tui-pane-requests.md).
34. <a id="source-34"></a> [TUI pane chrome](../digests/tui-pane-chrome.md).
35. <a id="source-35"></a> [TUI close confirmation](../digests/tui-close-confirm.md).
36. <a id="source-36"></a> [TUI pane prefix](../digests/tui-pane-prefix.md).
37. <a id="source-37"></a> [TUI empty panes, tab moves and docking](../digests/tui-pane-moves.md).
38. <a id="source-38"></a> [TUI pane menu](../digests/tui-pane-menu.md).
39. <a id="source-39"></a> [Chat participants, settings and attachment](../digests/chat-participants.md).
40. <a id="source-40"></a> [Chat reading and selection](../digests/chat-reading.md).
41. <a id="source-41"></a> [TUI re-attach and view reopen](../digests/tui-reattach.md).

42. <a id="source-42"></a> [Host and Lima diagnostics](../digests/host-diagnostics.md).

43. <a id="source-43"></a> [Shared Markdown renderer](../digests/markdown-renderer.md).

44. <a id="source-44"></a> [Per-TUI space selection](../digests/tui-space-switch.md).

45. <a id="source-45"></a> [Live file view](../digests/follow-view.md).

46. <a id="source-46"></a> [Disconnected chat input and reopen](../digests/chat-interaction.md).

47. <a id="source-47"></a> [Chat image previews](../digests/chat-image-previews.md).
