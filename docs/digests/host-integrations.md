# Explicit host image paste and browser requests

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Managed tabs/standalone attachment offer local Ctrl+V PNG reads and
confined HTTP(S) requests. The private session protocol carries bounded image
frames and targeted browser requests/results outside canonical VT sequencing,
never via terminal output. Passthrough and ordinary exec exclude these actions.
[1](#source-1) [2](#source-2) [3](#source-3) [12](#source-12)

[FACT] A holder runs a Python bridge through a second confined OpenShell SSH
pipe, without a new PTY, host daemon, mount or capability. Private
`/tmp/yoloctl-browser` retains `open-url`, `xdg-open` and its lock across holders.
`BROWSER` defaults to `open-url`; PATH gains the directory. Entry points publish
atomically. No-follow opens, ownership/mode checks and a stable native flock
guard the endpoint; only its owner binds/removes the socket. EOF removes the
socket; a successor recovers a crashed endpoint. Cached helper paths work with
the new holder, but interrupted requests never retry. No live bridge means an
explicit error. Missing Python leaves attachment available. The readiness read
cancels independently of peer EOF; an unanswered optional bridge does not keep
attachment waiting indefinitely.
[4](#source-4) [5](#source-5) [13](#source-13) [14](#source-14) [16](#source-16)

[FACT] In a divided TUI tab a request is matched to its pane; an unfocused
pane is marked [24](#source-24).

[FACT] Browser routing chooses one accepting managed view with the latest input
activity; ties choose the last attached. One request can be pending per holder.
Only its chosen view can answer; disconnect or 90 seconds cancels it without
retry/rerouting. There is no replay. Host `seams.browser` defaults to `on`;
explicit `ask`/`off` persist; unknown values use `ask`. Ask immediately opens
the palette/dialog with source, URL and Ctrl+O Open / Esc Cancel. A shared popup
renderer supplies rounded borders, palette colors, cell-width padding and a
reserved action footer. Pending asks queue without changing tabs; dismissal
restores focus/drafts. Ordinary typing, paste and Enter cannot approve.
Requests reject credentials, non-web schemes, controls,
invisible characters and URLs beyond 8192 bytes. The host runs fixed argv for
`xdg-open` or macOS `/usr/bin/open`, with no shell interpolation. No callback
port or localhost translation is created. [1](#source-1) [3](#source-3)
[6](#source-6) [7](#source-7) [23](#source-23)

[FACT] `seams.clipboard` defaults on and also gates copy-out/OSC 52 writes. Only a locally received Ctrl+V in a
managed surface starts the read: installed Wayland `wl-paste`, otherwise X11
`xclip` when DISPLAY is set, or fixed macOS AppleScript. Linux requests offered
targets, preferring a single local
PNG file URI, then PNG pixels, then an absolute PNG pathname; macOS tries an
alias before PNG. Missing images/backends preserve the encoded key pair.
There is no guest clipboard-read request. A successful image consumes its
press/release and inserts a guest path, quoted where necessary, bracketed when
requested, without Enter. Preparation reserves ordered input so later keys
cannot overtake it. Text paste preserves PNG paths literally; Ctrl+Shift+V does
not trigger image import. [3](#source-3) [6](#source-6) [8](#source-8)
[10](#source-10)
[15](#source-15) [20](#source-20)

[FACT] Copied files resolve to canonical host paths. Only descendants of this
box's declared host workdir qualify for reuse. A private probe sends path/size/
SHA-256; confined SSH opens the workdir and walks relative components without
following links, checking a regular readable file and matching bytes. A hit
pastes the mirrored guest path without uploading or consuming upload budget;
a miss copies the validated PNG. Reused files can subsequently change. Ephemeral
workdirs and unrelated mounts are not mapped. [1](#source-1) [4](#source-4)
[20](#source-20) [21](#source-21) [22](#source-22)

[FACT] PNGs decode within 16 MiB and 16 megapixels. The holder admits
one upload per view and 64 MiB cumulatively per session. A confined SSH writer
creates a random `/tmp/yoloctl-image-…/clipboard.png`, directory 0700/file 0600,
with exclusive/no-follow writes and file/directory fsync. There is no supplied
destination. Caught partial-write failures are cleaned up; completed files remain in
the container until removed or the box is replaced. They are not account state.
Upload timeout/detach can leave an uncertain file; a canceled blocked transfer
closes only its attachment, never retries, and preserves the holder/other views.
Unanswered SSH opens retain a slot until resolved, bounded to two.
[1](#source-1) [4](#source-4) [6](#source-6)

[FACT] Local tests cover PTYs/private descriptors, image bytes/input ordering,
real TUI prompt rendering across repeated decisions/cancellation/disconnect,
and inherited BROWSER in a surviving process across clean/crashed bridges.
The Linux smoke checks reconnect, holder restart and ask/default opening through
VM/controller/holder/sandbox with substituted desktop reader/opener. Its presence
does not prove a live pass. Native clipboard, provider image recognition and
OAuth need separate acceptance; macOS decoding tests are synthetic.
[9](#source-9) [10](#source-10) [11](#source-11)
[16](#source-16) [17](#source-17) [18](#source-18) [19](#source-19)

## Sources

1. <a id="source-1"></a> [host_actions.go](../../internal/session/host_actions.go#L73) — routing, HostClient lifecycle and bounded imageReceiver.
2. <a id="source-2"></a> [view.go](../../internal/session/view.go#L147) — private admission/delivery.
3. <a id="source-3"></a> [host_actions.go](../../internal/tui/host_actions.go#L22) — prompts, approval and ordered image key ownership.
4. <a id="source-4"></a> [host_bridge.go](../../internal/session/host_bridge.go#L36) — startBrowserBridge and browserLaunch.
5. <a id="source-5"></a> [holder.go](../../internal/session/holder.go#L153) — optional integration setup.
6. <a id="source-6"></a> [host.go](../../internal/hostintegration/host.go#L22) — validators, desktop dispatch, bounds and deadlines.
7. <a id="source-7"></a> [tui.go](../../internal/config/tui.go#L217) — policies and commented defaults.
8. <a id="source-8"></a> [input.go](../../internal/tui/term/input.go#L60) — Transform reserves ordered input.
9. <a id="source-9"></a> [host_actions_test.go](../../internal/session/host_actions_test.go#L1) — protocol/cancellation tests.
10. <a id="source-10"></a> [host_actions_test.go](../../internal/tui/host_actions_test.go#L198) — TestBrowserPromptsSurviveRepeatedRequestsAndDisconnect and key/paste tests.
11. <a id="source-11"></a> [host-integration.py](../../scripts/smoke/host-integration.py#L1) — isolated transport driver.
12. <a id="source-12"></a> [protocol.go](../../internal/session/protocol.go#L39) — version and action frames.
13. <a id="source-13"></a> [host_bridge.py](../../internal/session/host_bridge.py#L71) — serve and helper lifecycle.
14. <a id="source-14"></a> [OpenShell v0.0.72 ssh.rs](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-process/src/ssh.rs#L899-L971) — pipe exec confinement; source rather than live evidence.
15. <a id="source-15"></a> [input_test.go](../../internal/tui/term/input_test.go#L1) — preparation ordering/cancellation.
16. <a id="source-16"></a> [host_bridge_test.go](../../internal/session/host_bridge_test.go#L122) — bridge restart and filesystem regressions.
17. <a id="source-17"></a> [host_test.go](../../internal/hostintegration/host_test.go#L1) — desktop boundary tests.
18. <a id="source-18"></a> [host-integration-probe.py](../../scripts/smoke/host-integration-probe.py#L1) — guest audit.
19. <a id="source-19"></a> [test_host_integration.py](../../scripts/test_host_integration.py#L1) — local probe tests.
20. <a id="source-20"></a> [image_file.go](../../internal/hostintegration/image_file.go#L17) — single-file parsing, canonical reading and shell quoting.
21. <a id="source-21"></a> [image_reuse.py](../../internal/session/image_reuse.py#L8) — confined read-only content verification.
22. <a id="source-22"></a> [image_file_test.go](../../internal/hostintegration/image_file_test.go#L19) — file references, aliases, special files and quoting tests.
23. <a id="source-23"></a> [popup.go](../../internal/tui/popup.go#L9) — shared popup frame, wrapping and palette background.
24. <a id="source-24"></a> [TUI pane requests](tui-pane-requests.md) — requests matched to panes, attention marks.
