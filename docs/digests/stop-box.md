# Stop-box: start modes, the main-process launch and old boxes

Verification: `549d46495e13d1814aff3358c3a68fc7463211be` and accompanying changes; endpoint composition `fa98952c5e4b96e47b02211d14cfadaf76df66d0` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The fresh reservation that creates a current-runtime box writes a
`launcher` record (`1\n`, private) beside `origin.json`; an existing box is
never reserved again, so a later build never adds it. Reading it is strict:
absent means "created before", any other failure is an error, never "old".
Native cleanup admits it at the container root only in that exact shape (a
directory, link, other mode or near name in its place refuses the cleanup
before any unlink). [1](#source-1) [2](#source-2)

[FACT] A managed launch (an agent or a declared `session_command`) reads its
box's start mode before the holder starts; a plain shell reads nothing.
Confined box with the record: **main** (the holder's `--start-main`). Without
it, or on the old upstream runtime: needs recreate, warned, **exec**. A
privileged box: **marked** (`--mark-main`) when `agentbox.ProbeMainSession`
(`yoloctl-main-session`, empty, reply required, on a fresh channel then
closed) is accepted; an explicit refusal needs a restart; unavailable
transport is an error. [3](#source-3) [4](#source-4)

[FACT] A box whose launch config says `main_launcher` (set at preparation from
its record) runs the supervisor with `-i --main-exit-marker
/.openshell/supervisor/run/main-exit --` and the fixed `LauncherScript`, run
by the first of `/bin/bash --posix -c`, `/usr/bin/bash --posix -c`, `/bin/sh
-c` that is an executable regular file in the container (upstream's shell
order); others keep `sleep infinity`. The helper's source closure embeds
`internal/mainlaunch`. [12](#source-12) [13](#source-13)

[FACT] The launcher (no `mkfifo` → `sleep infinity`) makes a 0700 directory
and a 0600 FIFO in the box user's home, reads one record and removes both. It
accepts only `TERM=…`, a 32-hex token, the command, the token line, a name
list and the token as last line (exactly two token lines after the header);
otherwise it exits 93 and runs nothing. A listed name it lacks: one line
naming it, exit 94. It removes inherited names not listed (shell identifiers
by `unset`, others by `env -u`, skipped if that `env` cannot), keeps `TERM`
from the record, then runs the command with `-lc` in the login shell, by
`exec`: the command is the main process. Names with a control character are on neither side; values
are never copied or put in argv. The launcher's own shell assigns no
variable: its work runs in a subshell that answers a refusal or a quoted
script (unset, `TERM`, `exec`) that the shell evaluates, so every inherited
value and the file-creation mask reach the session unchanged, and the login
shell is `$0`, the image shell the supervisor selected.
[14](#source-14)

[FACT] The holder's main start opens a raw session channel (ssh.Session
cannot wait on a subsystem), requests the terminal, `OPENSHELL_MAIN_RAW_INPUT=1`
(patch 0005) and the `openshell-main` subsystem (a refusal: startup failure,
no marker), then the browser bridge, its marker and one hand-over: an exec of
the fixed `HandoverScript` with `OPENSHELL_NO_LOGIN_SHELL=1` and the record
(`TERM`, token, `exec` + today's command) on stdin; the script appends its own
environment's names and the token. Status 0: delivered, input opens. 81 (no
`mkfifo`: directive 61's refusal), 82 (no FIFO), 83 (a name holds the
token): proven not delivered, marker removed. On 81 the holder also writes
its refusal record (`refused`, beside the marker); the caller removes any
earlier one before each holder start and, when the holder dies during
startup, reads it and fails with `box NAME's image has no mkfifo (install
coreutils in the image); a session cannot be started in it`. Anything else: not known,
marker kept, ends as a lost connection. Never sent twice. A marked start
sends `yoloctl-main-session` before today's exec; a refusal starts nothing.
[15](#source-15)

[FACT] An attach that starts a plain shell (no agent, no `session_command`)
prints one info line: the box has no session process, the shell is temporary,
the box keeps running after it, and how to stop it. [3](#source-3)

[FACT] A privileged box that needs a restart is restarted once, with an info
line, at its next managed session start when one complete read of the VM's
/proc shows no process in its PID namespace besides PID 1 and the same run
before and after; otherwise it warns and starts with exec. [4](#source-4) [5](#source-5)

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

[FACT] Tests use fakes, real shells for the launcher and hand-over (under
`/bin/sh` and `bash --posix`, including the `BASH_ENV` hook seen once as by
today's `bash -lc`) and an isolated current endpoint with PID1 disabled;
nothing here was run on a VM. [7](#source-7) [9](#source-9) [14](#source-14) [15](#source-15)

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
12. <a id="source-12"></a> [internal/openshell/launch.go](../../internal/openshell/launch.go#L1) — `MainLauncher`, `SupervisorArgs`, `LauncherShells`, `SupervisorMainExitMarker`; `launch_test.go`.
13. <a id="source-13"></a> [internal/openshell/launcher_shell_linux.go](../../internal/openshell/launcher_shell_linux.go#L1) — `launcherShell`; `runtime_exec_linux.go`, `prepare_linux.go`; `helperinputs.go`.
14. <a id="source-14"></a> [internal/mainlaunch/mainlaunch.go](../../internal/mainlaunch/mainlaunch.go#L1) — `LauncherScript`, `HandoverScript`, `Record`, `NewToken`, statuses; `mainlaunch_linux_test.go`.
15. <a id="source-15"></a> [internal/session/handover.go](../../internal/session/handover.go#L1) — `openMainChannel`, `handOver`, `markMainSession`, `mainChannel.wait`; `holder.go` `RunHolder`; `holder_main_test.go`; the refusal record in `tombstone.go`, `unit.go` `ReadVMRefusal`, `cmd/yoloctl/session_client.go` `waitForAttachSocket`, `session_refusal_test.go`.
