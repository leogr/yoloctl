# Sessions after a controller update

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] On a running space, `space start --update-controller` redeploys only the
controller. Running agent sessions keep their holder, which compares a peer's
bundle and terminal core in every handshake and refuses another build with
`session build/core mismatch`. Views of the new build therefore cannot attach to
them until their box restarts, or is recreated when it also has recreation
drift; the attach error names which ([remedies](session-remedies.md)).
[1](#source-1)

[FACT] `session.ReadStats` uses the read-only stats handshake, which admits no
viewer. Validity is checked before compatibility. `ErrBuildMismatch` matches
only an explicit, well-formed answer of another build: a one-byte HELLO with
another protocol version, the stats refusal matched byte for byte before any
display sanitizing, or a complete reply (bundle and session) naming another
bundle. Each keeps its message. A malformed HELLO, a refusal that differs in any
byte, an incomplete reply (including this bundle without a session), another
refusal, a close or silence does not match. [1](#source-1) [4](#source-4)

[FACT] Native resolver probes add `session.build` (`current`, `other` or
`unknown`, for any failure to ask) and `session.holder` to a running session.
The holder identity is the PID and start time from the marker every build's
holder writes at startup, or empty. Sessions that are not running, and probes
outside the VM, omit both. `box.inventory` carries them to the host.
[2](#source-2) [4](#source-4)

[FACT] After a successful update, the host releases its lifecycle lock and reads
that inventory and `chat.status`. Sessions it could not check or identify
(`unknown`, `other` without a holder, or a declared `session_command` when the
room cannot be read) are named as left running. Boxes that also require
recreation are set aside next with their explicit commands, also left running
([remedies](session-remedies.md)). It lists the remaining `other` boxes, warning that a restart ends the agent's current process and that its
conversation must be resumed explicitly. The room's server box is named as
restarting the room for every connected chat client, and another
`session_command` box as a managed workload started again. Only when stdin and
stderr are real terminals (isatty; `/dev/null` is not) does it ask once
(`[y/N]`, default No). Otherwise it restarts nothing and prints each
`yoloctl box restart NAME --space SPACE --no-attach`, followed for the chat
server by `yoloctl chat ensure --space SPACE` and for another workload by
`yoloctl box attach NAME --space SPACE`; a declined prompt prints the same list.
The shared `isInteractive` gate makes the same terminal check for its other
prompts and `space ssh`. `--restart-sessions`, which requires
`--update-controller` and is checked before the lock, restarts without asking.
[3](#source-3) [4](#source-4)

[FACT] Each restart is bound to its listed holder. Before each one the host
lists again and skips, leaving running, a box whose session is no longer the
same `other` holder. It then sends an ordinary `box.restart` with `--no-attach`,
its own ID and the hidden `--if-stale-session HOLDER`. Under the space operation
lock that excludes other managed mutations, box restart checks that precondition
before any other step and refuses a stopped, replaced, current or unknown
session without stopping it. Otherwise it runs the shared session-aware stop and
start; it is not a recreate. An agent box then has no session until someone
attaches or resumes it. For another `session_command` box the request adds the
hidden `--start-workload`, which box restart checks before teardown (the box
must declare a workload other than the chat server's `ChatServerCommand`) and
honours afterwards through `ensureAgentSession`, the session start attach uses.
Right after the chat server's restart the host sends the ordinary `chat.ensure`
request once, never repeated. A successful exit is not enough: the host decodes
the bounded, typed status of its final observation and accepts only a room whose
server reports ready. An absent room or an unready server (with its pending
reasons) is a failure; participant changes still pending behind a ready server
are shown. Only then does it say every connected chat client must reconnect. The
first failure, including a chat ensure that fails or reports the server not
ready, or a failed workload start, stops the sequence with an error (a nonzero
exit), prints the workload's recovery command, names the boxes not attempted and
keeps an unknown outcome for receipt review; nothing is retried. A failed
listing restarts nothing: it is a warning, or an error when `--restart-sessions`
was given. [3](#source-3) [4](#source-4)

[FACT] Regressions cover holder classification (including malformed answers),
holder identity, the resolver fields, the listing, the default-No prompt,
`/dev/null`, script output, the opt-in flag, replacement during the prompt and
before a batch entry, the precondition before teardown, chat ensure and workload
starts after their restarts, failed ensure and workload starts, an ensure whose
status reports the server pending (through the production closure and real
controller framing) beside a ready control, lost replies to the chat restart and
ensure, workload listings, failure handling and the shared request. They use
fakes; no live VM update was run. [4](#source-4)

## Sources

1. <a id="source-1"></a> [stats.go](../../internal/session/stats.go#L47-L149) — `serveStats`, `ErrBuildMismatch`, `buildMismatch`, `holderMismatchReason`, `ReadStats`, `readStats`; view refusal in holder_view.go L243.
2. <a id="source-2"></a> [boxstate.go](../../internal/boxstate/boxstate.go#L61-L485) — `SessionBuild`, `SessionInfo`, `NewResolver`, `ProbeHolder`, `sessionBuild`; marker `Identity` in session/tombstone.go L32 and `ReadVMMarker` in unit.go L105; `box.inventory` in controller_views.go L256.
3. <a id="source-3"></a> [controller_sessions.go](../../cmd/yoloctl/controller_sessions.go#L29-L417) — `sessionRestartOffer`, `hostSessionRestartOffer`, `sessionRestartRequest`, `chatEnsureRequest`, `chatEnsureOutcome`, `staleSessions`, `offerSessionRestarts`, `checkStaleSession`, `requireStaleSession`, `restartableWorkload`, `startRestartedWorkload`; `defaultChatHuman` in chat.go L87, `ensureManagedServer` in chat_runtime.go; caller `hostControllerLifecycle` in controller_lifecycle.go L64-L111, preconditions and workload start in agentbox.go `newAgentboxRestartCmd` L2122-L2152, `isInteractive` in agent.go L328.
4. <a id="source-4"></a> [Restart offer regressions](../../cmd/yoloctl/controller_sessions_test.go#L1) — with `TestReadStatsClassifiesOnlyExplicitBuildAnswers` and `TestMarkerIdentityNeedsPIDAndStart` in session/stats_test.go L278-L363, and `TestSessionBuildFollowsTheHolderAnswer` in boxstate_test.go L211.
