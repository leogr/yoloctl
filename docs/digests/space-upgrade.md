# Upgrading a space

Verification: `b54fc365f39f3319e9fd680789ed8318d84e45bb` and accompanying changes.

[FACT] Host-only `space upgrade [SPACE]` resolves the space like `space start`.
It selects the invoking build, with the existing VM compatibility, separate VM
recreation consent and controller startup checks. The host lifecycle lock is
released before box planning, prompts or requests. [1](#source-1) [6](#source-6)

[FACT] The upgrade combines the controller's box inventory, managed room status
and full drift in groups of at most 64 declared boxes. Each group has a shared
30-second budget. A timed-out group remains outstanding. The plan leaves
missing containers unmaterialized and reports unavailable state and orphans.
[2](#source-2) [3](#source-3)

[FACT] Invocation authorizes holder-bound restarts for compatible stale
sessions. Container replacement and other running policy restarts require
separate named, default-No answers on terminal input and terminal stderr. All
answers are collected before the first box change. For running boxes with only
port drift, upgrade prints the named reconcile command. Stopped boxes with
known drift stay stopped without a prompt. Upgrade prints the start command
for saved policy or port changes, or the recreate command if replacement is
required. When the only replacement reason is an old session launcher, upgrade
still restarts a matching stale session in the existing container after declined
or unanswered replacement consent. An ordinary restart can create a container
that vanishes after the last read. The pre-native restart still stops and
refuses the old run, reporting the separate recreation remedy.
[2](#source-2) [7](#source-7)

[FACT] Upgrade rechecks each approved target and uses the ordinary explicit
recreate path with a worker-side declaration, container and mode binding.
Replacement loses container-only files and keeps workdir, account state and the
separate cache. Privileged
launches retain host-only authority. A previously active managed workload runs
again. The selected room's server is handled last and restored with one
room-bound ensure that must report ready. Connected clients must reconnect and
re-arm delivery. [2](#source-2) [4](#source-4) [5](#source-5) [7](#source-7) [8](#source-8)

[FACT] A submitted No or blank default-No answer leaves the action declined.
Upgrade warns and lists its follow-up command, and succeeds if there are no
unresolved actions or execution failures. EOF, unrecognized answers and lack
of a terminal leave consent unanswered. Those actions and unresolved running
box actions are reported separately and give a nonzero result. A lost reply
or other execution failure stops dispatch without retry and preserves the
operation error. An interrupted replacement image build ends the upgrade
without a retry prompt. A later explicit invocation reads state again.
[2](#source-2)

## Sources

1. <a id="source-1"></a> [controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L73) — `hostControllerLifecycle`.
2. <a id="source-2"></a> [space_upgrade.go](../../cmd/yoloctl/space_upgrade.go#L125) — `readSpaceUpgradePlan`, `classifySpaceUpgradeBox`, `runSpaceUpgrade`, `recheckSpaceUpgradeRow`.
3. <a id="source-3"></a> [session_remedy.go](../../cmd/yoloctl/session_remedy.go#L141) — `collectBoxDriftView`.
4. <a id="source-4"></a> [controller_sessions.go](../../cmd/yoloctl/controller_sessions.go#L98) — `ensureControllerChat`, `sessionRestartRequest`, `startRestartedWorkload`.
5. <a id="source-5"></a> [space_upgrade_target.go](../../cmd/yoloctl/space_upgrade_target.go#L92) — `readUpgradeRecreateBinding`, `checkUpgradeRecreateTarget`.
6. <a id="source-6"></a> [space_upgrade.go](../../cmd/yoloctl/space_upgrade.go#L80) — `finishRepairedSpaceUpgrade`.
7. <a id="source-7"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L2378) — `newAgentboxRestartCmd` and `recreateAgentbox`.
8. <a id="source-8"></a> [managed_applier.go](../../cmd/yoloctl/managed_applier.go#L87) — `Recreate`.
