# Stop-box: start modes, old boxes and the removed reaper

Verification: `549d46495e13d1814aff3358c3a68fc7463211be` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The fresh reservation that creates a current-runtime box writes a
`launcher` record (`1\n`, private) beside `origin.json`; an existing box is
never reserved again, so a later build never adds it. Reading it is strict:
absent means "created before", any other failure is an error, never "old".
Native cleanup admits it at the container root only in that exact shape (a
directory, link, other mode or near name in its place refuses the cleanup
before any unlink). [1](#source-1) [2](#source-2)

[FACT] A managed launch (an agent or a declared `session_command`) reads its
box's start mode before the holder starts; a plain shell reads nothing.
Confined box with the record: main-process start. Without it, or on the old
upstream runtime: needs recreate, warned, exec start. Privileged box: exec
start while this build has no main-session request
(`agentbox.PrivilegedMainSession` is nil); with one, a running endpoint that
refuses it needs a restart. `agentbox.ProbeMainSession` asks that request
once (`yoloctl-main-session`, empty, reply required, on a fresh session
channel that is then closed) but is not wired while the endpoint does not
serve it. The holder still starts every session with exec:
the main-process hand-over is route M-b's section 3, not built here.
[3](#source-3) [4](#source-4)

[FACT] An attach that starts a plain shell (no agent, no `session_command`)
prints one info line: the box has no session process, the shell is temporary,
the box keeps running after it, and how to stop it. [3](#source-3)

[FACT] A privileged box that needs a restart is restarted once, with an info
line, at its next managed session start when one complete read of the VM's
/proc shows no process in its PID namespace besides PID 1 and the same run
before and after; otherwise it warns and starts with exec. Inactive while
`PrivilegedMainSession` is nil. [4](#source-4) [5](#source-5)

[FACT] Directive 56's warning is a resolver reason (`launcher_recreate`,
`main_session_restart`), never drift; every one-box command except recreate,
destroy, list, create and copy prints it once, whatever its result. Before
dispatch a host terminal asks "Recreate now? [y/N]" for a new managed session
in a box that needs recreate (joining a running session asks nothing); yes
sends one ordinary recreate, then the request. [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] The resolver reads the container first: a box that is not running is
`stopped`, its last exit or lost session beside it. A stopped container with
a marker and no holder (read through the same marker probe for tracked boxes)
records the lost launch, removes the marker and starts a fresh session; a
failed probe refuses and removes nothing. [6](#source-6) [3](#source-3) [9](#source-9)

[FACT] The agent daemon reaper is gone: no `--reap-daemon`, no launch
wrapper, no `session.daemons` in the Codex profile. A custom profile that
still declares it loads with a warning per load and loses it at its next
save. Until boxes stop with their session, a Codex app-server can outlive a
launch. [10](#source-10) [11](#source-11)

[FACT] Tests use fakes; nothing here was run on a VM. [7](#source-7) [9](#source-9)

## Sources

1. <a id="source-1"></a> [internal/openshell/launcher_capability_linux.go](../../internal/openshell/launcher_capability_linux.go#L1) — `LauncherCapabilityName`, `ContainerLauncherCapable`.
2. <a id="source-2"></a> [internal/openshell/reserve_linux.go](../../internal/openshell/reserve_linux.go#L89) — `reserveOwnedContainerAt` writes the record after `origin.json`; `reservation_cleanup_linux.go` `reservationMember` admits it; tests in `reserve_linux_test.go` and `reservation_cleanup_linux_test.go`.
3. <a id="source-3"></a> [cmd/yoloctl/session_client.go](../../cmd/yoloctl/session_client.go#L1) — `ensureAgentSession`, `sessionStartModeOf`, `restartPrivilegedForMainSession`, stopped-box marker handling.
4. <a id="source-4"></a> [internal/agentbox/launcher_linux.go](../../internal/agentbox/launcher_linux.go#L1) — `BoxLauncher`, `PrivilegedMainSession`; `launcher.go` `LauncherSupport`; `main_session_probe_linux.go` `ProbeMainSession` and its tests.
5. <a id="source-5"></a> [internal/agentbox/launcher_idle_linux.go](../../internal/agentbox/launcher_idle_linux.go#L1) — `OtherContainerProcesses`.
6. <a id="source-6"></a> [internal/boxstate/boxstate.go](../../internal/boxstate/boxstate.go#L1) — reasons, `ProbeSession`, `deriveDisplay`.
7. <a id="source-7"></a> [cmd/yoloctl/launcher_warning.go](../../cmd/yoloctl/launcher_warning.go#L1) — `registerStartModeWarnings`; tests in `launcher_warning_test.go`, `expected_run_test.go`.
8. <a id="source-8"></a> [cmd/yoloctl/recreate_offer.go](../../cmd/yoloctl/recreate_offer.go#L1) — `launcherRecreateOffered`, `sendOfferedRecreate`; `recreate_offer_test.go`.
9. <a id="source-9"></a> [cmd/yoloctl/session_identity_test.go](../../cmd/yoloctl/session_identity_test.go#L1) — `TestTrackedStoppedBoxRecordsItsLostHolder`.
10. <a id="source-10"></a> [internal/agentprofile/profile.go](../../internal/agentprofile/profile.go#L1) — `LegacyDaemons`, `LegacyDaemonsNotice`.
11. <a id="source-11"></a> [cmd/yoloctl/cli_logging.go](../../cmd/yoloctl/cli_logging.go#L1) — the per-load warning.
