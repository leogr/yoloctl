# Managed sessions, attachment, and replay

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; default title `1d265df3` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `make build` produces the host CLI and static Linux amd64/arm64 companions.
Native and Darwin host targets link the companions' SHA-256 values as runtime
variables, independent of Go build-info/VCS metadata and `-trimpath`. A common bundle ID is stamped once per Make invocation and checked by the
controller protocol. Guest compilation excludes host `-race` instrumentation but retains other `GOFLAGS`.
[11](#source-11) [12](#source-12)

[FACT] Guest architecture selects the companion, resolving host symlinks first.
Companions must be static Linux ELF executables matching the linked hash.
Unstamped Linux development builds use themselves; a same-architecture Linux
host copied without its companion also uses itself. Dynamic self-copies must
pass a bounded guest startup check before installation. That internal check
returns before configuration initialization. Non-Linux hosts require a verified
bundle. [10](#source-10) [13](#source-13) [28](#source-28)

[FACT] One guest probe reads architecture and installed hash. Matching content
skips transfer; otherwise an exclusively created private staging directory,
checksum verification and atomic rename preserve live holder inodes. Cleanup
uses a locally generated path, never parsed remote stdout. The holder still runs
`session-hold`, not a separate executable. [10](#source-10)

[FACT] Explicit space start/update deploys the selected bundle before controller
admission. Native workers reuse that verified executable for holders. Ordinary
attach requires a matching controller even when a holder already lives; it does
not install a local companion or automatically upgrade the controller.
[5](#source-5) [14](#source-14)

[FACT] A VM-side holder owns the agent's sandbox SSH connection and PTY.
Controller workers connect directly to the holder's local Unix socket;
closing one client closes that transport, not the holder's SSH child. The
holder runs as an independent guest service with a fixed per-box name, which
also prevents concurrent starts. Lima uses a transient systemd unit; Firecracker
uses its guest supervisor. It is not configured to restart and adopt an old
agent after holder loss. [1](#source-1) [2](#source-2) [3](#source-3) [9](#source-9) [26](#source-26) [27](#source-27)

[FACT] Control sockets live under `/run/yoloctl/sessions`; markers and last-exit
state live under `/var/lib/yoloctl/sessions`. These directories are separate
from the box's mounted sandbox SSH socket and account state. Reserved box names keep session/controller/operator directories distinct. [4](#source-4)

[FACT] The holder writes its active marker before starting the SSH child and
removes it if startup fails. A known child exit writes a tombstone and removes
the marker; uncertain connection loss leaves the marker. CLI session startup
uses these artifacts plus unit/container status to detect orphaned sessions
rather than blindly spawning another agent. Periodic client keepalives retain
idle SSH transport. [3](#source-3) [5](#source-5)

[FACT] The session protocol validates exact build/core identity. The holder owns
canonical terminal state and query replies. Managed views restore a complete
native snapshot then ordered events; standalone interactive attach uses the
same renderer without TUI chrome. Compatibility `--passthrough` retains a
bounded, query-filtered transcript. Both modes remain interactive, with
independent bounded output queues. Native snapshots exclude historical effects.
[1](#source-1) [6](#source-6) [17](#source-17) [24](#source-24)

[FACT] Before PTY output or client admission, the holder initializes the title
from the template with an empty `{title}`; literal separators remain. Quiet
agents thus identify their box. `{title}` alone disables initialization and
composition. Later OSC 0/1/2 updates replace the initial title; other terminal
bytes are preserved. Managed snapshots and bounded passthrough replay retain
titles. [3](#source-3) [7](#source-7) [25](#source-25)
The worker expands `{box}`, `{space}`,
`{agent}`, `{account}`, `{agent-icon}` and at most one `{title}` from host
preferences. Control characters, invalid placeholders and extra `{title}` tokens
introduced by literal expansions (including across boundaries) are rejected.
With `{agent-icon}`, leading matching icons/spaces are stripped. Composition
buffers only where required; malformed/unfinished controls flush without loss.
[7](#source-7) [20](#source-20) Preferences apply at holder start; existing
`tui.yaml` values are preserved. The default is
`{agent-icon} {account}\{box}: {title}`. [5](#source-5) [19](#source-19)
Only active icons receive agent color. Grapheme matching prefers the longest
prefix, then agent name. Padding reserves two columns plus a separator before
truncation/hit testing. Width negotiation and Unicode/layout tests do not
establish graphical font appearance. [18](#source-18) [21](#source-21)
[22](#source-22) [23](#source-23)

[FACT] `finishAttach` treats transport cancellation as detach with an actionable
space-qualified hint, while known agent exit status propagates. Reattaching to
a living holder is distinct from restarting an agent and reopening its saved
history. Persisted-history regression checks use separate processes and native
storage; terminal replay itself is not persistence validation. [5](#source-5)
[8](#source-8)

[FACT] The smoke harness bounds final PTY draining, retains raw captures and
escapes controls in diagnostics. Local tests cover exit races, inherited
slaves, timeout cleanup, and actual Bash setup/resume commands with
echo-independent readiness and preserved shell state.
[15](#source-15) [16](#source-16)

## Sources

1. <a id="source-1"></a> [internal/session/view.go](../../internal/session/view.go#L147) — `attachView`.
2. <a id="source-2"></a> [internal/session/dial.go](../../internal/session/dial.go#L21) — `DialVM`.
3. <a id="source-3"></a> [internal/session/holder.go](../../internal/session/holder.go#L76-L290) — `RunHolder`, initial title before output/admission.
4. <a id="source-4"></a> [internal/session/session.go](../../internal/session/session.go#L1-L107) — socket/state layout and ring/keepalive defaults.
5. <a id="source-5"></a> [cmd/yoloctl/session_client.go](../../cmd/yoloctl/session_client.go#L42) — holder setup, tuning and finishAttach.
6. <a id="source-6"></a> [internal/session/protocol.go](../../internal/session/protocol.go#L25) — frame types, `readFrame`, `writeFrame`.
7. <a id="source-7"></a> [internal/session/title.go](../../internal/session/title.go#L1-L308) — `titleRewriter`, `InitialTitle`, `ExpandBoxTitleTemplate`.
8. <a id="source-8"></a> [scripts/smoke/native-large-append.py](../../scripts/smoke/native-large-append.py#L1-L86) — `record`, `verify`, `append_batch`, `main`.
9. <a id="source-9"></a> [internal/session/unit.go](../../internal/session/unit.go#L1-L117) — `StartHolderUnit`, holder unit probes and stop.
10. <a id="source-10"></a> [internal/session/binary.go](../../internal/session/binary.go#L20) — `EnsureVMBinary`, transfer scripts and `guestBinary`.
11. <a id="source-11"></a> [Makefile](../../Makefile#L16) — shared build identity and host/guest build targets.
12. <a id="source-12"></a> [scripts/guest-hashes.go](../../scripts/guest-hashes.go#L1-L35) — native hash/linker-flag generation.
13. <a id="source-13"></a> [cmd/yoloctl/main.go](../../cmd/yoloctl/main.go#L27) — side-effect-free loader check before CLI initialization.
14. <a id="source-14"></a> [cmd/yoloctl/controller_deploy.go](../../cmd/yoloctl/controller_deploy.go#L115) — explicit selected bundle deployment.
15. <a id="source-15"></a> [scripts/smoke/pty-run.py](../../scripts/smoke/pty-run.py#L27) — `diagnostic_output`, `read_pty`, `drain_pty`, `main`.
16. <a id="source-16"></a> [scripts/test_pty_run.py](../../scripts/test_pty_run.py#L24) — shared smoke fixture, scheduled race, unmocked output/exit, control leakage and timeout regressions.
17. <a id="source-17"></a> [Authoritative state and compatibility replay](session-replay.md) — canonical snapshots, compatibility limits, output queues and tests.
18. <a id="source-18"></a> [tabicon.go](../../internal/tui/tabicon.go#L1-L98) — icon matching, padding, coloring and width-aware truncation.
19. <a id="source-19"></a> [internal/config/tui.go](../../internal/config/tui.go#L131-L339) — defaults, `GetAgentIcons`, `AgentIconGlyphs`, `LoadTUI` preservation.
20. <a id="source-20"></a> [title_test.go](../../internal/session/title_test.go#L170-L236) — literal expansion and generic-box contracts.
21. <a id="source-21"></a> [tabicon_test.go](../../internal/tui/tabicon_test.go#L1-L214) — inactive styles, icon spacing, negotiation, truncation and shared-glyph stability.
22. <a id="source-22"></a> [tui.go](../../internal/tui/tui.go#L2202) — `tabSegments`, `tabStripLabels`, `tabStrip`, `tabAt`.
23. <a id="source-23"></a> [width negotiation](../../internal/tui/tui.go#L760) — `Update` mirrors renderer mode-2027 support.
24. <a id="source-24"></a> [Managed view transport](session-views.md) — frontend selection, private descriptors and protocol identities.
25. <a id="source-25"></a> [holder_title_test.go](../../internal/session/holder_title_test.go#L10) — `TestHolderInitialTitleAndAgentUpdates`: silent startup, templates, live updates and both attachment modes.
26. <a id="source-26"></a> [internal/vmservice/service.go](../../internal/vmservice/service.go#L1) — guest service selection
27. <a id="source-27"></a> [supervisor](../../internal/vmguest/units_linux.go#L1) — holder exclusion, cleanup and no automatic restart.

28. <a id="source-28"></a> [Guest artifact selection](../../internal/guestbinary/binary.go#L1) — linked hashes, `Resolve`, `LinuxExecutable`, shared by boot-image preparation and session deployment.
