# Agent daemon lifetime

Verification: `62581f36477d3036d3883123b85e5f8570776066` and accompanying test-only changes; re-read cited symbols when changing behavior.

[FACT] A profile's `session.daemons` lists the agent's own background servers
that outlive its exit, each an argv prefix whose first word is compared to the
executable's base name. An entry is one to eight words, separated by single
spaces, of letters, digits and `._+-`, starting with a letter or digit;
anything else fails the profile. Codex declares `codex app-server`.
[1](#source-1) [2](#source-2)

[FACT] The Codex TUI starts a detached app-server daemon and a separate
`codex app-server daemon pid-update-loop`, each in its own session, and a
later TUI reuses a healthy daemon without comparing its environment. The
daemon has no idle exit. [9](#source-9) [10](#source-10) [11](#source-11)
[12](#source-12)

[FACT] A managed launch is the lifetime of its agent's declared daemons:
- **Plumbing:** every launch of an agent box, manual ones included, passes
  the profile's daemons to the holder as repeated `--reap-daemon` arguments;
  the holder refuses an entry that fails the profile rule before it connects.
- **Before the agent:** the agent's own session command starts with
  GNU coreutils `timeout -s KILL 5 sh -s` reading the reaper from a quoted
  here-document,
  in a `{ … } 3>&2 2>/dev/null` group, then the agent's exec. The group
  discards both shells' diagnostics, bash's killed-command report included,
  and ends before the exec: nothing is added to the agent's argv, environment
  or descriptors, its stderr is unchanged and typeahead stays queued. timeout runs the reaper in its own process group
  and kills the whole group at 5 s, so a stalled scan delays the agent by at
  most 5 s and no child of the reaper outlives it, a TERM-ignoring one
  included; a child blocked in an uninterruptible read ends when the read
  does. A timed-out reaper prints `yoloctl: agent daemon reaper timed out`
  to the agent's terminal.
- **After the agent:** once the holder observes the agent's exit status, it
  runs the reaper in a second session, bounded at 10 s with the channel
  opening, before the tombstone.
  A holder stop or a lost connection never reaps.
[3](#source-3) [4](#source-4) [5](#source-5) [6](#source-6)

[FACT] The reaper is a POSIX sh program. It matches processes whose `Uid:`
equals its own and whose `/proc/<pid>/cmdline` starts with a declared prefix;
a near miss (a longer word, a single argument containing the space, a prefix
after another argv[0]) and zombies do not match. It sends SIGTERM, waits up to
2 s, sends a second SIGTERM to survivors, waits up to 1 s, then sends SIGKILL
and waits up to 1 s, polling every 0.1 s and re-matching each PID before every
signal. Its only output is a line naming processes that survived SIGKILL,
on fd 3 with the shells' own messages discarded: before the agent it reaches
the agent's terminal, after the exit the holder log. The launch proceeds either way. [3](#source-3) [7](#source-7) [8](#source-8)

[FACT] Limits:
- The match cannot tell launches apart, so a declared daemon a user started
  from `box exec` is stopped too.
- Processes the daemon started are stopped only when they match a prefix.
- Codex's first SIGTERM drains turns; its forced shutdown returns without
  waiting for its transports, so a killed daemon can leave its socket file.
  A turn still running may not be saved.
[3](#source-3) [13](#source-13) [14](#source-14)

[FACT] The Linux stalled-scan integration tests select verified GNU
`timeout` or `gnutimeout` through their private fixture PATH, matching the
Ubuntu 24.04 managed base. Missing GNU timeout fails explicitly; another
host implementation is not substituted for its process-group contract.
The launch-budget, no-survivor and PTY typeahead assertions remain active.
[7](#source-7) [15](#source-15)

## Sources

1. <a id="source-1"></a> [Profile](../../internal/agentprofile/profile.go#L167-L172) — `SessionSpec.Daemons`, `DaemonArgv` (L589), `SessionDaemons`, `validateSession` (L545).
2. <a id="source-2"></a> [Codex profile](../../internal/agentprofile/builtin/codex.yaml#L91-L95) — `session.daemons`.
3. <a id="source-3"></a> [Reaper](../../internal/session/reap.go#L35-L182) — wait constants, `reapLaunchBudget`, `reapExitBudget`, `reapDelimiter`, `reapScript`, `ReapScript`, `reapBefore`, `reapAfterExit`.
4. <a id="source-4"></a> [Holder](../../internal/session/holder.go#L108) — `RunHolder`: refusal, the prefixed start (L223), the reap before the tombstone (L363).
5. <a id="source-5"></a> [Holder unit](../../internal/session/unit.go#L68-L100) — `holderUnitArgs`.
6. <a id="source-6"></a> [Session client](../../cmd/yoloctl/session_client.go#L529) — `sessionDaemons`.
7. <a id="source-7"></a> [Reaper regressions](../../internal/session/reap_linux_test.go#L93) — real processes under sh and bash: SIGTERM, second SIGTERM, SIGKILL, near misses, typeahead; a stalled, TERM-ignoring scan bounded and ended under sh and bash, under a PTY; `findGNUReapTimeout`, `TestFindGNUReapTimeout`, `stallingScan` select the matching GNU dependency (L159–239).
8. <a id="source-8"></a> [Holder regressions](../../internal/session/reap_test.go#L33) — the prefix and its delimiter (L33), both reap points (L67), no reap on stop or loss.
9. <a id="source-9"></a> [Codex 0.165 daemon argv](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/app-server-daemon/src/backend/pid.rs#L391) — server and pid-update-loop command lines.
10. <a id="source-10"></a> [Codex 0.165 detach](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/app-server-daemon/src/backend/pid_start.rs#L159) — each in its own session.
11. <a id="source-11"></a> [Codex 0.165 daemon start](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/app-server-daemon/src/lib.rs#L437) — reuse of a probed endpoint.
12. <a id="source-12"></a> [Codex 0.165 disconnect](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/app-server/src/lib.rs#L1215) — no idle exit.
13. <a id="source-13"></a> [Codex 0.165 signals](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/app-server/src/lib.rs#L226) — the first SIGTERM drains, the second forces.
14. <a id="source-14"></a> [Codex 0.165 forced shutdown](https://github.com/openai/codex/blob/rust-v0.165.0/codex-rs/app-server/src/lib.rs#L1393) — returns without waiting for transports.
15. <a id="source-15"></a> [Managed base](../../assets/containers/agentbox/Dockerfile#L6) — Ubuntu 24.04.
