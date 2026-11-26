# Upgrading a space

Verification: `b54fc365f39f3319e9fd680789ed8318d84e45bb` and accompanying changes; VM replacement check `e3b21585fc6a31b400f01455a70838d8ebdbabb7` and accompanying changes.
Helper drift verified at `8a5e840af94251837f9ad6e9bded0b026b458070` and accompanying changes.
Older-controller drain verified at `cc47f7f92e43347b68d4856b4315b0ee93ec0bc6` and accompanying changes.

[FACT] Host-only `space upgrade [SPACE]` resolves the space like `space start`.
It selects the invoking build, with the existing VM compatibility, separate VM
recreation consent and controller startup checks. The host lifecycle lock is
released before box planning, prompts or requests. [1](#source-1) [6](#source-6)

[FACT] After the VM compatibility check and before the controller update,
upgrade compares the VM with this build using install advice's read-only
selections. A Firecracker VM matches when its recorded image is this build's
published default image. A Lima VM matches when its effective container-stack
archives equal this build's template pins. Upgrade reports problems proving
the Lima archives, and other Lima template differences, as not detectable. It
never boots, prepares or downloads media for this check. For a mismatch,
upgrade shows the reason and recreate command and asks once, default No, on
terminal input and stderr. A yes runs the compatibility repair's `space
recreate --force` (with `--update-vm-image` on Firecracker) and continues with
the repaired box review. A no warns with the command and keeps success. No
terminal, EOF or another answer leaves it outstanding with a nonzero result.
The answer is read byte by byte, so later box answers stay unread.
[9](#source-9) [10](#source-10)

[FACT] The controller update drains the running controller before installing
this build, also after a No. A released protocol-2 controller (v0.1.0 to
v0.3.0) refuses this build's hello with exit 2 before reading any request; only
then the host sends that one drain again in protocol 2, with the same bundle,
space and generation, `MatchHello`'s identity checks and no protocol-3 request
field. The old controller answers once its drain has finished, so its
operations end first. Every other request keeps the exact protocol-3
handshake. [14](#source-14) [15](#source-15)

[FACT] The upgrade combines the controller's box inventory, managed room status
and full drift in groups of at most 64 declared boxes. Each group's drift
view has one partial-data minute on one shared inventory, and reads each box's
upgrade target as soon as that box's own drift is resolved, so a slow box
cannot hide a healthy one. A box not resolved within the minute remains
outstanding with its evidence unavailable; the others keep their rows. The plan leaves
missing containers unmaterialized and reports unavailable state and orphans.
[2](#source-2) [3](#source-3)

[FACT] The upgrade drift read compares a confined container's verified
OpenShell helper source and dependencies against this build. A mismatch
gives `needs_recreate` and names the earlier-build helper in the plan and
consent prompt. A stopped box gets the reason and a follow-up command.
Unavailable helper evidence leaves the action outstanding.
[2](#source-2) [3](#source-3) [11](#source-11) [12](#source-12) [13](#source-13)

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

1. <a id="source-1"></a> [controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L71) — `hostControllerLifecycle`.
2. <a id="source-2"></a> [space_upgrade.go](../../cmd/yoloctl/space_upgrade.go#L125) — `readSpaceUpgradePlan`, `classifySpaceUpgradeBox`, `runSpaceUpgrade`, `recheckSpaceUpgradeRow`.
3. <a id="source-3"></a> [session_remedy.go](../../cmd/yoloctl/session_remedy.go#L161) — `collectBoxDriftView`.
4. <a id="source-4"></a> [controller_sessions.go](../../cmd/yoloctl/controller_sessions.go#L98) — `ensureControllerChat`, `sessionRestartRequest`, `startRestartedWorkload`.
5. <a id="source-5"></a> [space_upgrade_target.go](../../cmd/yoloctl/space_upgrade_target.go#L92) — `readUpgradeRecreateBinding`, `checkUpgradeRecreateTarget`.
6. <a id="source-6"></a> [space_upgrade.go](../../cmd/yoloctl/space_upgrade.go#L80) — `finishRepairedSpaceUpgrade`.
7. <a id="source-7"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L2447) — `newAgentboxRestartCmd` and `recreateAgentbox`.
8. <a id="source-8"></a> [managed_applier.go](../../cmd/yoloctl/managed_applier.go#L87) — `Recreate`.
9. <a id="source-9"></a> [space_upgrade_vm.go](../../cmd/yoloctl/space_upgrade_vm.go#L39) — `readSpaceVMReplacement`, `offerSpaceUpgradeVMRecreate`, `confirmSpaceUpgradeVM`; install_migration.go `firecrackerImageState`, `readLimaStackState`; call site in controller_lifecycle.go `hostControllerLifecycle`; result reporting in space_upgrade.go `runSpaceUpgrade`.
10. <a id="source-10"></a> [space_upgrade_vm_test.go](../../cmd/yoloctl/space_upgrade_vm_test.go#L1) — Firecracker/Lima selection cases, consent matrix, exit status, and the order before the controller update.
11. <a id="source-11"></a> [space_upgrade_target.go](../../cmd/yoloctl/space_upgrade_target.go#L52) — `readUpgradeTarget` binds the helper check to the inspected container.
12. <a id="source-12"></a> [openshell_native_linux.go](../../internal/agentbox/openshell_native_linux.go#L50) — `OpenShellHelperNeedsRecreate` verifies the native runtime record.
13. <a id="source-13"></a> [release.go](../../internal/openshellrelease/release.go#L31) — `EarlierHelper` compares verified helper inputs.
14. <a id="source-14"></a> [older_drain.go](../../internal/controller/older_drain.go#L10-L92) — `OlderProtocol`, `RefusedAsOlder`, `DrainOlder`; host use `drainBeforeDeployment` in cmd/yoloctl/controller_deploy.go, called by `startControllerSpaceImpl` through `drainControllerForDeployment`.
15. <a id="source-15"></a> [older_drain_test.go](../../internal/controller/older_drain_test.go#L17-L241) — v0.3.0 wire fixture (strict frame decoding, its `MatchHello`), `TestUpgradeDrainsAReleasedProtocol2Controller`, `TestOlderDrainKeepsTheExactHandshake`, `TestRefusedAsOlderMatchesOnlyTheReleasedRefusal`.
