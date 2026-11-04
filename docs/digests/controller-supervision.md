# Controller process supervision

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] Firecracker PID 1 supervises the fixed root controller launcher separately
from session holders, port units and lazy BuildKit. An unrequested nonzero or
fatal-signal exit triggers reaping and complete controller-cgroup cleanup. Only
verified empty cleanup permits a replacement; each launch prepares a fresh
cgroup. Clean exit and requested stop never restart. A handled signal resulting
in exit zero is a clean exit. No failure here shuts down the VM. [1](#source-1)
[2](#source-2)

[FACT] The first retry waits five seconds **after cleanup**, then subsequent
attempts wait 10, 20, 30 and 30 seconds. Failed automatic spawns consume attempts
and require cleanup too. Five exhausted attempts latch `failed`; elapsed time
alone cannot reopen it. An initial explicit spawn error returns an error without
automatic retry. Explicit start after a finished clean or exhausted run opens a
fresh budget. Start during an existing run is idempotent and cannot bypass its
backoff or reset its budget. Cleanup failure prevents further starts, including
explicit ones; VM replacement provides a fresh supervisor. [1](#source-1)
[3](#source-3)

[FACT] At least 60 seconds of continuous process uptime resets the retry budget,
evaluated from the reaped process's exit timestamp, excluding cleanup time.
This measures process survival, not health or readiness. A controller crashing
after 70 seconds can repeatedly receive a first retry after cleanup plus five
seconds, indefinitely. Guest logs retain cumulative abnormal exits, automatic
launch attempts (including failed spawns), and monotonic failure-episode numbers
across uptime resets and explicit stop/start. Episodes increment on a new
failure sequence, not when time merely passes. Restart/reset/exhaustion logs
include those counts and the episode's attempt; counters reset with PID 1 and
are never stored on the native data disk. [1](#source-1) [3](#source-3)

[FACT] One run owns launch, exactly one Wait per child, cleanup, timers and
cancellation. Stop cancels first, sends TERM when a child exists, allows ten
seconds for exit, then kills/verifies the whole controller cgroup and joins the
child. The total stop budget is 20 seconds. An unjoined run cannot be replaced;
a concurrent start while stopping receives a retryable state error. Old callbacks
never clean a successor's cgroup. Poweroff and every shutdown entry latch a
terminal prohibition on start before signalling processes. [1](#source-1)
[3](#source-3) [4](#source-4) [11](#source-11)

[FACT] Service state is `active` for a live process, `activating` for launch,
cleanup or backoff, `deactivating` while stopping, `inactive` after clean or
requested exit, and `failed` after launch/cleanup failure or exhaustion. A past
process failure does not make a failed-state read error; unknown cleanup can.
These are supervision states, not controller readiness. Failed drain permits
host deployment only on positively `inactive`/`failed` service state; backoff
and stopping cannot be mistaken for permission to replace unavailable work.
[1](#source-1) [5](#source-5) [12](#source-12)

[FACT] Replacement invokes ordinary controller startup: singleton, verified
store, receipt recovery, maintenance and endpoint restoration. It queues or
replays no requests, clears no receipt/image holds, changes no VM incarnation,
and proves no BuildKit solve/export termination. Independent holders can survive;
client streams may disconnect and require explicit reattachment. A read-only
recovery server remains alive without a health restart. [6](#source-6)
[7](#source-7) [10](#source-10) [13](#source-13) [14](#source-14)

[FACT] Existing Firecracker spaces need explicit `space recreate NAME
--update-vm-image`: PID 1 lives in the selected image. Recreate preserves native
data but loses sessions and ephemeral workdirs. Controller-only updates cannot
install this policy; old/custom selected images retain their behavior until
explicit image replacement. Automatic recipes already hash init bytes. Lima's
unchanged systemd unit uses `Restart=on-failure`, `RestartSec=5`; its other restart
rules are not claimed identical. There is no watchdog for hung/live controllers,
VM restart, or recovery guarantee for PID 1/VM death or unkillable workers.
[8](#source-8) [9](#source-9) [15](#source-15)

## Sources

1. <a id="source-1"></a> [Controller supervisor](../../internal/vmguest/controller_linux.go#L1) — launcher, `start`, `watch`, `wait`, `stop`, counters and states.
2. <a id="source-2"></a> [Guest units](../../internal/vmguest/units_linux.go#L65) — `prepareGroup`, `cleanGroup`.
3. <a id="source-3"></a> [Supervisor tests](../../internal/vmguest/controller_linux_test.go#L1) — virtual-clock transitions, episode boundaries, cleanup failures, concurrent stop/start and owned real children.
4. <a id="source-4"></a> [Guest dispatch](../../internal/vmguest/init_linux.go#L526) — `service`, `shutdown`.
5. <a id="source-5"></a> [Deployment guard](../../cmd/yoloctl/controller_deploy.go#L122) — `drainControllerForDeployment`.
6. <a id="source-6"></a> [Controller startup](../../cmd/yoloctl/controller_internal.go#L279) — `serveController`.
7. <a id="source-7"></a> [Recovery proof rules](controller-recovery.md) — permitted evidence and holds.
8. <a id="source-8"></a> [VM image selection](vm-images.md) — explicit replacement and retained media.
9. <a id="source-9"></a> [Lima controller unit](../../internal/controller/service.go#L5) — `ServiceUnit`.

10. <a id="source-10"></a> [Restart compatibility regression](../../internal/controller/restart_test.go#L18) — durable receipts/maintenance and explicitly simulated degraded-store admission across server reconstruction.
11. <a id="source-11"></a> [Orphan reaper](../../internal/vmguest/reap_linux.go#L20) — `startOwned`, `reapOrphans`.
12. <a id="source-12"></a> [Deployment guard tests](../../cmd/yoloctl/space_image_update_test.go#L175) — activating/deactivating refusal and failed-state deployment.
13. <a id="source-13"></a> [Receipt startup](../../internal/controller/receipts.go#L145) — `Recover`.
14. <a id="source-14"></a> [Lazy BuildKit](../../internal/vmguest/buildkit_linux.go#L77) — independent service ownership.
15. <a id="source-15"></a> [Image recipe](../../internal/firecracker/recipe_linux.go#L18) — `defaultRecipe` hashes init bytes.
