# Managed attachment frontends and private transport

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Interactive managed attachment defaults to the shared TUI renderer.
Inside yoloctl it occupies a tab; standalone attachment uses the same model,
core, key encoder, mouse/selection, colors and clipboard policy without chrome.
The configured palette key (default Ctrl+Y), then Ctrl+W detaches; doubling the
prefix forwards it. An explicit browser `ask` opens a dialog immediately;
Ctrl+O/Esc answers without a prefix. Other prefix combinations reach the child.
A notice can overlay the last row until the next non-modifier key. Child exit
status reaches the invoking shell. [1](#source-1) [2](#source-2) [3](#source-3) [20](#source-20)

[FACT] The CLI owns process signals and cancels the frontend context. Both
standalone attachment and the main TUI disable Bubble Tea's independent signal
handler and translate cancellation into graceful quit with a bounded input-loop wait.
Shutdown restores terminal settings and reporting modes before returning;
the second handler must not block sending into an already stopped event loop.
[2](#source-2) [16](#source-16) [17](#source-17) [18](#source-18)

[FACT] `--passthrough` opts out on attach, run and lifecycle commands that
may auto-attach. It is an invocation flag, not persisted box state. Inside a
TUI tab, compatibility bytes still pass through the enclosing renderer.
Nonterminal attachment also uses compatibility output. `box exec` remains the
scripted-command/byte-output interface. Multiple views retain input; changing
frontend does not change the holder's canonical terminal identity or query
ownership. [1](#source-1) [4](#source-4) [5](#source-5)

[FACT] The session protocol uses a versioned HELLO/subscription handshake with exact build
identity and embedded core SHA, followed by state metadata, bounded snapshot
chunks and sequenced live events. Clipboard effects have separate bounded
chunk/end framing and never interrupt unfinished VT controls. Older holders or
mismatched builds fail explicitly; updating the controller alone does not
replace an existing holder. End its agent session and start a fresh one.
[5](#source-5) [6](#source-6) [7](#source-7)

[FACT] A locally invalid resize closes only that attachment and preserves its
validation error in bounded synchronized state. Zero columns or rows report the
measured grid and advise setting a nonzero terminal size before attaching again.
Managed and passthrough views share this path; valid grids with unknown pixels
remain accepted. Cancellation and received exit/refusal results retain priority;
remote closure without a local validation failure still reports holder-stream
loss. Local holder/protocol tests cover initial and later invalid resizes,
reattachment, cancellation and real exit status 137. They do not prove that a
simultaneously failing holder survived. [6](#source-6) [7](#source-7) [23](#source-23)

[FACT] In the TUI one managed session can show in two panes of a tab; they
are two viewers with the shared-size rule. Closing a pane is a detach. [22](#source-22)

[FACT] Managed subscriptions advertise browser acceptance; state metadata
advertises image upload support. These live private actions use separate
bounded frames, without advancing VT event sequence or replaying side effects.
Host clipboard reads require a local paste gesture. [20](#source-20)

[FACT] Snapshots are never decoded from command stdout. The trusted CLI child
inherits a private Unix socket; controller protocol 2 relays this channel
separately from stdout/stderr/stdin, with independent bounded input credits.
Only registered managed-session methods can request it. The worker inherits
another private descriptor and relays the holder frames, observing final status.
Descriptors are close-on-exec before starting descendants. The existing
operation registry, grant validation, release callback and receipts still
apply. No new daemon or box-mounted session socket is introduced.
[1](#source-1) [8](#source-8) [9](#source-9) [10](#source-10)

[FACT] Before snapshot publication, ordinary PTY output renders setup progress.
While attached, only private events can mutate the replica. Its native
callback never sends query replies; local color reports become metadata and
local measured geometry waits for the canonical event. [19](#source-19) User input stays on the original
ordered PTY/stdin path through setup and attachment; the worker forwards it
after admission. Publication never switches input paths or strands typeahead.
Snapshot replacement discards setup
filters/local selection. Ordinary exec/editor commands can leave the private
channel unused and continue through their PTY. Closing a view cancels its
local client and preserves the persistent agent. [2](#source-2) [11](#source-11)

[FACT] After the private stream and PTY pumps finish, the completed view uses
its last local dimensions before appending the exit banner. Later resizes stay
local. This keeps status/hint visible in a smaller view while preserving the
final screen, replica query suppression and live shared-resize policy.
Main/alternate-screen process regressions cover this transition.
[11](#source-11) [13](#source-13)

[FACT] Real socket/RPC/worker/PTY tests cover flow control, cancellation,
separate streams, setup/results, input ordering, stdout forgery, frontend
routing, geometry, keys, notices and exit status. Signal tests cover termios,
mode cleanup and continued output draining through process exit with a 68 KiB
post-render trailer; protocol tests reject incompatible/damaged state and event
gaps. These are local tests, not disposable-Lima or graphical acceptance.
[15](#source-15)
[3](#source-3) [12](#source-12) [13](#source-13) [14](#source-14) [17](#source-17)

[FACT] `--box-use` selects a structured managed frontend with bounded plain-text
observations and explicit text/key input. It shares the private transport,
follows canonical geometry and reports advisory peer activity. Default managed
and passthrough routing remain separate. [21](#source-21)

[FACT] When a standalone managed attachment's command fails before a holder
view exists (interactive create/start/attach setup), the temporary screen that
showed the diagnostic is already gone when the attachment returns, so the
returned error carries the bounded (4 KiB) plain-text tail of that last setup
screen, without the frontend's exit banner or control characters. The child's
exit status is preserved for callers; the tail is display output only, never
session protocol or operation evidence. Success, detach and an established
holder view are unaffected. [24](#source-24)

## Sources

1. <a id="source-1"></a> [controller_client.go](../../cmd/yoloctl/controller_client.go#L318) — `callControllerTerminal`, recursive frontend guard and request routing.
2. <a id="source-2"></a> [attach.go](../../internal/tui/attach.go#L18) — `RunAttached`, standalone input/detach; shared model in `tui.go`.
3. <a id="source-3"></a> [attach_test.go](../../internal/tui/attach_test.go#L11) — standalone PTY key/mouse and notice regressions.
4. <a id="source-4"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L116) — shared attach help and `addPassthroughFlag`.
5. <a id="source-5"></a> [Holder and replay](session-replay.md) — canonical state, mixed viewers and compatibility limitations.
6. <a id="source-6"></a> [view.go](../../internal/session/view.go#L20) — subscription, `Relay`, snapshot/event validation and clipboard framing.
7. <a id="source-7"></a> [protocol.go](../../internal/session/protocol.go#L25) — session version, frame bounds and status.
8. <a id="source-8"></a> [channel.go](../../internal/termio/channel.go#L12) — private pollable socket pair and inherited descriptor validation.
9. <a id="source-9"></a> [server.go](../../internal/controller/server.go#L304) — separate view queues, worker streams and receipt completion; client counterpart in `client.go`.
10. <a id="source-10"></a> [worker.go](../../internal/controller/worker.go#L24) — private worker descriptor and bounded output drain; request validation in `protocol.go`.
11. <a id="source-11"></a> [managed.go](../../internal/tui/term/managed.go#L26) — private attachment and replica publication; PTY ownership in `session.go` and `emulator.go`.
12. <a id="source-12"></a> [view_test.go](../../internal/controller/view_test.go#L17) — real RPC/worker/PTY stream, status and cancellation tests.
13. <a id="source-13"></a> [managed_process_test.go](../../internal/tui/term/managed_process_test.go#L20) — inherited-channel fixture, `TestManagedExitFitsLocalViewport`, PTY forgery and typeahead; state contracts in `managed_test.go`.
14. <a id="source-14"></a> [controller_view_test.go](../../cmd/yoloctl/controller_view_test.go#L19) — shared CLI routing, flags and exit status.
15. <a id="source-15"></a> [view_test.go](../../internal/session/view_test.go#L17) — ordered buffered stdin, EOF/status preservation and malformed state/event rejection.
16. <a id="source-16"></a> [signals.go](../../cmd/yoloctl/signals.go#L20) — `commandSignalContext` owns process signals.
17. <a id="source-17"></a> [signals_tui_test.go](../../cmd/yoloctl/signals_tui_test.go#L22) — real signal context/frontends, termios and mode cleanup; private acknowledgment precedes late output, drained concurrently with process exit.
18. <a id="source-18"></a> [tui.go](../../internal/tui/tui.go#L159) — `Run` and shared `runProgram` use graceful context-driven shutdown without a second signal handler.
19. <a id="source-19"></a> [Measured geometry](terminal-geometry.md) — native query state, snapshots, font changes and compatibility limits.
20. <a id="source-20"></a> [Host integrations](host-integrations.md) — upload/browser admission, explicit host policies and private transport tests.
21. <a id="source-21"></a> [Box-use](box-use.md) — structured interface, collaboration, bounds and tests.
22. <a id="source-22"></a> [TUI pane requests](tui-pane-requests.md) — one session in two panes, closing a pane.
23. <a id="source-23"></a> [attach_geometry_test.go](../../internal/session/attach_geometry_test.go#L98) — initial/later local geometry failures, unchanged canonical holder, valid reattachment, cancellation and remote closure/status controls in both modes.
24. <a id="source-24"></a> [Standalone setup failure](../../internal/tui/attach.go#L1) — `attachedCommandResult`, `attachedSetupOutput`; `TestStandaloneSetupFailureRetainsActualChildDiagnostic` in internal/tui/attach_setup_error_test.go (sol standalone-setup-error tree `633f37ea`).
