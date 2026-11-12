Every lifecycle smoke sets an owned `YOLOCTL_HOME`, ignoring an inherited
value. Common setup reads roots from `yoloctl version --json`; children inherit
the same home. Never reuse a real space's home as a smoke fixture.

# Live-VM smoke tests

## macOS vmshare unit verification

From a host-owned checkout/worktree of the candidate on macOS, run:

```sh
bash scripts/check-vmshare-macos.sh
```

This runs the complete vmshare package three times under each umask (022 and
077), then its race tests under each mask. It records the commit, dirty status,
macOS/Go versions, inherited mask and actual Go/host architectures, including
an amd64 Go toolchain running under Rosetta. It retains logs in a new temporary
directory and returns nonzero if any run fails. Go's race detector needs a
working C toolchain. The mask changes are confined to child processes. No
yoloctl binary, VM, space, mount or credentials are used; these are local SFTP
and filesystem tests, not mounted SSHFS/Firecracker qualification.

## Linux runtime closeout

For a short Linux amd64 check of the bundled Linux 7 kernel and Firecracker
TCP/UDP forwarding (including both TCP half-close directions), OCI Feature
declaration/copy, typed options and completion, run
`python3 -B scripts/smoke/runtime-closeout.py` after `make build`. It creates a
private temporary space, reuses verified host downloads, then removes the VM
and retains its small report. The OCI checks fetch a pinned public artifact;
they do not execute its installer or build agent images. Codex's bubblewrap
packaging is covered by `smoke-image-ensure`, not this quick check. Initial VM image
preparation can take longer on a cold cache. Failed fixtures stay at their
original paths with an explicit cleanup command; do not move them.

## Chat image previews (human-guided)

From a candidate checkout on the host, after `make build`, run in the terminal
under test (Ghostty or Kitty):

```sh
bash scripts/smoke/chat-images-live.sh
```

It always creates a new owned fixture root (inherited `SMOKE_ROOT` and
`SMOKE_SPACE` are ignored) with space `chat-images-live`, a managed chat room
and an agentless enrolled participant, which publishes four fixture PNGs to
`#space` through the real `airc artifact upload` path: an alpha gradient, a
wide image, a non-PNG named `.png` and a PNG over the 16 Mi-pixel decode
bound. The operator then opens the TUI on the same terminal and answers finite
rows: draw, aspect, chip copy, re-show, error notices, resize/tab switch and
quit cleanup; the same with `COLORTERM` removed (no Show image); standalone
`yoloctl chat` as its own image host (draw, replace, quit cleanup); and, optionally, a terminal without Kitty
graphics. Each launched UI's exit status is its own row. The Markdown report
beside the fixture root records OS, terminal name/version, `COLORTERM`,
yoloctl bundle and checkout, the fixture, every row, the cleanup outcome and a
disposition: COMPLETE (exit 0) only when every required row passed, every UI
exited normally and cleanup succeeded or was explicitly preserved; FAILED
(exit 1) for a FAIL row, an abnormal UI exit, a setup error or failed cleanup;
INCOMPLETE (exit 3) for a skipped, unanswered or interrupted required row, or
a terminal other than Ghostty/Kitty. Cleanup is `space destroy --force`, whose
acknowledged success is the only teardown evidence, then removal of the
fixture root, checked absent; either failing makes the run FAILED and the
report names the retained path. `SMOKE_PRESERVE_SPACE=1`
keeps the space (recorded), and `SMOKE_KEEP_ON_FAILURE=1` keeps a failed one.
A shown image proves the real controller read through the reserved chat
runtime's relay and policy. The re-show row is visual only; nothing here
measures timing, memory or download counts, and it covers only its named rows,
not the full terminal, ACL, performance and forgery matrix.

## Lima container stack gate

From a clean reviewed candidate checkout on the macOS host, run:

```sh
bash scripts/smoke/lima-stack.sh
```

Requires installed Lima/SSH, a Go launcher supporting toolchain selection
(Go 1.21+), make, Git, Python 3, network
access for public downloads and capacity for one 4-CPU/6-GiB VM (30-GiB system
and 10-GiB native disks). It refuses Linux, box/controller contexts, dirty
tracked source and unknown arguments. Host tools must already exist; it uses
no host sudo, credentials or prompts. Inherited `SMOKE_*`, `YOLOCTL`,
`YOLOCTL_HOME`, XDG and Lima roots cannot select existing targets. Caller,
runner, yoloctl and build `HOME` stay unchanged. An owned process-local
`limactl` wrapper execs the pre-resolved real Lima binary with a private child
home (`LIMA_STACK_CHILD_HOME`), isolating macOS downloads under that home's
`Library/Caches/lima`. Direct, nested smoke and cleanup Lima calls share this
wrapper; the runner copies no user cache, SSH configuration or credentials. The target is always `stack-qa` in a new short `/private/tmp/yls-*`
fixture. The runner sets `YOLOCTL_HOME` to that fixture's `home`, verifies the
built binary's config/cache roots with read-only `version --json`, and passes
the same home to nested smokes and recovery commands. Existing normal Lima homes are allowed; overlapping/symlink paths and
isolated `_config/default.yaml` or `override.yaml` cause refusal.

Before building, a bounded Go preflight records the installed launcher and
checks `go env GOVERSION` / `go version` with the selected toolchain. It selects
the exact stable `toolchain` directive when present, otherwise the `go` version
from `go.mod` (language `1.27` selects release `go1.27.0`). Go may download that
release using its standard proxy and checksum verification, entirely in the
fixture's private Go module/build caches. `GOENV=off` stays set; no proxy or
checksum relaxation is introduced. An unavailable or mismatched toolchain stops
before `make` or VM work with the host version, requirement, selected release
and installation/download recovery instructions. The report retains these Go
observations; read-only `--check` does not run this preflight or download Go.

The script builds once into the fixture, retaining matching companion hashes
and bundle ID. It creates a synthetic-account space, asserts the effective
architecture-specific nerdctl-full 2.3.5 archive and guest nerdctl 2.3.5,
containerd 2.3.3, runc 1.5.1 and buildctl/buildkitd 0.31.2, and checks live
services/controller. It runs `controller-operator.sh` with preserve-space
ownership, rechecks the pins/versions and native disk identity after that
script's VM recreation, then checks JSON boolean `HostConfig.Privileged: false`,
verified confined mode, operator stop/start/restart without replacement, and a
policy-only PTY grant with errno-attributed denied/allowed confined exec.
Finally it builds a multi-stage feature (external image, FROM/RUN/COPY), checks
its marker via confined exec, explicitly rebuilds once and rechecks content/mode.
No provider authentication or agent calls occur. Existing classifier/guard and
negative source-policy unit tests remain separate requirements.

The overall bound is 60 minutes; build, provisioning, operator and image phases
are each capped at 30 minutes, lifecycle/observation at 5 minutes, individual
reads/exec at 2 minutes. Cleanup has a separate 5-minute grace. A timeout or
INT/TERM or unsuccessful command settles the transient command group even if
its leader has exited. Termination and pipe draining have finite grace periods.
Unverified group cancellation or pipe closure blocks fixture destruction and
retains recovery state with `CLEANUP_INCOMPLETE`; the original failure remains
the result. Successful VM launches retain their lifecycle-owned services.
Mutations are never retried. The script prints its fixture and separate
`/private/tmp/ylr-*` report path before VM work, then one PASS/FAIL summary. `report.json` schema 1 records
candidate/bundle/binary hashes, effective config, versions, disk identity,
the resolved real Lima executable/cache paths, downloaded cache entries and
independently checked archive checksum, command argv/log/exit statuses and phase
result/exit codes; `phases.tsv` is the
compact table. Numbered `.stdout`/`.stderr` files retain build and runtime logs.
After verified VM/disk destruction (or before any VM was created), fixture
removal makes only the owned Go module-cache directories owner-writable through
a descriptor-based no-follow walk. It then removes that exact fixture; it
never cleans a caller's Go cache or follows cache symlinks outside the fixture.
These reports are retained after successful fixture destruction. Failed or
uncertain destruction reports `CLEANUP_INCOMPLETE`, preserves matching binaries
and recovery paths, and stays nonzero. Diagnose before running the printed
exact recovery command. SIGKILL/host loss cannot guarantee cleanup; use the
initial printed ownership paths. No generic Lima/cache pruning occurs.

The separate `--check` mode observes an **already running** existing Lima space
with its matching bundle. It installs no QA wrapper or child-home override.
It requires explicit existing config/Lima roots and selects `YOLOCTL_HOME`
from `--config-root`, ignoring any inherited home. It queries the matching
binary's config/cache roots; the cache child may be absent and is not created.
It writes only a new report directory outside target/grant trees. It
never builds, boots, updates, recreates, exports, starts boxes or destroys a
target. Unavailable/stopped/mismatched targets produce nonzero UNKNOWN/FAIL;
missing inventories never count as preservation. It records disk ID/UUID,
space/box/account names/status, desired restart list, host/guest versions,
global-config presence/hashes and effective archives. `--compare` additionally
requires the pinned stack, matching disk/inventories and previously running
containers restarted. Restart expectations use `boxes[].dimensions.runtime`,
independently of exited/lost/drifted display labels. Display/session status and
runtime changes remain visible separately; missing/unknown/unavailable runtime
evidence in either report is a nonzero UNKNOWN result, including older reports
that did not capture that dimension. This observation does not
perform an upgrade or prove native-byte preservation.

For example, before a separately scheduled maintenance window, using the
currently matching `bin/` bundle and new report paths:

```sh
bash scripts/smoke/lima-stack.sh --check --space yolo \
  --binary "$PWD/bin/yoloctl" \
  --config-root "$HOME/Library/Application Support/yoloctl" \
  --output /private/tmp/yolo-lima-before
# After the explicit host upgrade and desired box starts, with the new matching bundle:
bash scripts/smoke/lima-stack.sh --check --space yolo \
  --binary "$PWD/bin/yoloctl" \
  --config-root "$HOME/Library/Application Support/yoloctl" \
  --output /private/tmp/yolo-lima-after \
  --compare /private/tmp/yolo-lima-before/report.json
```

For non-default roots, pass their actual existing config directory. The matching
binary must support the read-only root query. The selected home uses its `cache`
child regardless of the build channel or inherited XDG roots. Never rebuild
the old bundle before the before-snapshot. Save team state/needed container-local
files and consider a native export backup before disruptive maintenance. Follow
the [explicit Lima upgrade sequence](../../docs/digests/vm-adapters.md#lima-stack-upgrades).
The isolated gate never upgrades the active team space. Script tests with fake
tools prove routing and failure handling; they are not live Lima acceptance or
native amd64/Rosetta validation. Future Lima internals that write outside the
reviewed roots require investigation; they do not authorize a real-home fallback.

For a focused VM startup measurement with an already-built matching host/guest
bundle, run:

```sh
YOLOCTL="$PWD/bin/yoloctl" bash scripts/smoke/vm-startup.sh
```

This standalone probe selects Lima explicitly and rejects other backends. It
requires Lima, SSH and host Python 3. It allocates a fresh
temporary XDG config/cache root and workdir, rejecting inherited `SMOKE_ROOT`
or `SMOKE_WORKDIR`. It measures one complete `space create` and three
stop/start cycles with a monotonic clock, checks containerd/controller services
and account reads, and captures guest `/proc/meminfo`, installed dependency
versions, OpenShell checksum/inode/mtime and running services. Set
`VM_START_SAMPLES=1..20` to change restart count and
`VM_START_SETTLE_SECONDS=0..60` to change the ten-second delay before each
memory snapshot (outside the timing). Timings, command logs and snapshots are
retained in the printed report directory; the VM and fixture are removed.
It requests 4 GiB and 4 CPUs unless `SMOKE_VM_MEMORY`/`SMOKE_VM_CPUS` override the budget.
`SMOKE_PRESERVE_SPACE=1` retains the fixture too; `SMOKE_KEEP_ON_FAILURE=1`
retains it on failure. `make smoke-vm-startup` builds
a new bundle first; use the direct script when preserving an existing build.

Run the same probe against a baseline binary with matching companion binaries
on the same host. Compare creation and restart separately and repeat fresh
runs: downloads, host load and external caches affect creation times. CLI
duration includes provisioning and controller readiness, not only kernel boot.
`MemTotal - MemAvailable` estimates guest memory in use; it is not host VM RSS
or a changed VM memory allocation. Image and provisioning changes together
measure their combined effect, not the image's isolated contribution. This
probe creates no boxes; run the combined runtime gate below for image, policy,
account and session compatibility. Local script/unit checks are not live VM
validation or evidence of a speed or memory improvement.

`make smoke-live-vm` is the combined runtime gate. It creates a temporary XDG
config/cache root, provisions a dedicated space there, runs its seventeen
phases in this order, and purges the space and temporary files on exit. Failed
space cleanup retains the isolated recovery files and reports their location.
Set `SMOKE_KEEP_ON_FAILURE=1` for the combined gate, `smoke-image-ensure`, `smoke-airc`, `smoke-npm`, `smoke-pip`, `smoke-go` or `smoke-attach-interrupt` to retain its isolated VM and
logs on any test failure for investigation (successful runs still clean up).
Retention does not stop the VM. Keep the fixture directory in its original
location: it contains VM disks and configuration with absolute paths, not just
logs. Copy individual log files when sharing diagnostics. For a Linux fixture
that should survive a host reboot, select a short persistent `SMOKE_ROOT` under
`/var/tmp` before its first run; do not relocate an existing fixture.

The smoke harness explicitly defaults to Lima. For Linux amd64 Firecracker, set
`SMOKE_BACKEND=firecracker`; `SMOKE_VM_IMAGE=/absolute/prepared/image` optionally
reuses prepared media instead of building it inside the isolated cache.
Fixtures request 4 GiB and 4 CPUs explicitly; set `SMOKE_VM_MEMORY` or
`SMOKE_VM_CPUS` to change their budget.
The shared `smoke_ensure_space` helper returns a failed create or start command's
nonzero status immediately, before any optional kernel probe, including when
called with `errexit` disabled or in an `if` condition. With
`SMOKE_EXPECT_KERNEL_RELEASE` set, a failed kernel SSH probe also returns its
status even if its output matches; a successful probe with the wrong release
fails with the expected-kernel diagnostic. Successful setup returns 0.
See the [image and benchmark guide](../vm/firecracker/README.md). The
Lima-wrapper interruption injection is specific to Lima; the shared lifecycle,
image, confinement, account and session phases use the selected backend.

The Firecracker lifecycle phase selects this build's recipe image by default,
preparing it if absent (even if initial creation used `SMOKE_VM_IMAGE`).
`SMOKE_UPDATE_VM_IMAGE=/absolute/image` selects a distinct compatible replacement
for that phase. Image-update failure/recovery boundaries also have injected unit
tests; the live smoke does not simulate power loss. Host-share tests run through
the selected mount transport and verify the resulting bytes on the host.

The archive gate also accepts `SMOKE_IMPORT_BACKEND` to restore onto a different
adapter. Run both directions on a host with Lima and KVM:

```sh
SMOKE_BACKEND=lima SMOKE_IMPORT_BACKEND=firecracker scripts/smoke/space-archive.sh
SMOKE_BACKEND=firecracker SMOKE_IMPORT_BACKEND=lima scripts/smoke/space-archive.sh
```

For the complete backend acceptance matrix with an already built candidate:

```sh
YOLOCTL="$PWD/bin/portable-candidate/yoloctl" scripts/smoke/vm-backends.sh
```

This runs the full Firecracker and Lima gates (including same-backend archives),
then Lima → Firecracker and Firecracker → Lima archive round trips, sequentially.
It checks both backends before creating fixtures and allocates a fresh private
config/cache/workdir for each phase. It stops on the first failure, retains
failed fixtures for inspection, and writes phase logs plus `summary.tsv` beneath
the printed `/tmp/yb.*` directory. Short fixture subdirectories (`fc`, `li`,
`lf`, `fl`) leave room for Lima's restored instance names and temporary SSH
socket suffix; the runner deliberately ignores `TMPDIR` for this root.
Successful fixtures are destroyed;
their logs and archives remain private under that directory. Cleanup failure is
a failing result. Nothing rebuilds the candidate or changes its bundle identity.
`SMOKE_VM_IMAGE` can reuse prepared Firecracker media; omitting it also exercises
automatic image preparation in each isolated cache. The Python/shell smoke
drivers are development tools; they are not Firecracker runtime dependencies.
Lima retains its own `limactl`/SSH prerequisites. The backend matrix requires a
Linux amd64 host with usable KVM and Lima.

To run only the two cross-backend archive directions after the full gates have
passed, reuse the same candidate with fresh isolated fixtures:

```sh
YOLOCTL="$PWD/bin/portable-candidate/yoloctl" scripts/smoke/vm-backends.sh --archives-only
```

This does not resume or overwrite retained fixtures, and its report covers only
the two archive phases. No CLI rebuild is needed for smoke-script changes.

On macOS, automatically allocated fixtures use a short `/private/tmp/ys.*`
root to fit Lima's Unix socket path limit. An explicit `SMOKE_ROOT` must also
leave room for Lima's instance and socket names.

`make smoke-dev-privileged` is a separate gate for disposable hosts only; it
refuses to start without `SMOKE_DEV_PRIVILEGED_CONFIRM=1` or inside a box. It
checks that the retired `yolo.dev.privileged` capability is refused with
guidance, creates a `--privileged` box (space-VM root) beside a confined one and
checks runtime evidence, sudo root, capability/device/filter state, environment
and ownership, a holder session, ports, root-descendant stop, operator authority
(exec, restart and stop of the running box succeed; an operator start, ordinary
create and reconcile are refused; the host start warns about operator access)
and explicit recreation in both directions, then prints a PASS/FAIL table. The
holder phase uses the shared structured `box attach --box-use` client: it waits
for a runtime-built marker in the canonical screen before detaching, with a
bounded observation deadline and a required clean frontend exit. Requests,
stdout/stderr and the result are saved under `privileged-holder/` in the smoke
root; `SMOKE_KEEP_ON_FAILURE=1` retains that disposable root on failure. Local
fake-client regressions check the driver, not the privileged host runtime.
The port phase holds one foreground HTTP serving exec, proves an invocation-unique
response inside the box before requiring that same response through the host
forward, then sends a stdin stop handshake, requires the serving exec to exit
zero, and checks the listener is gone. Requests
bypass ambient proxies, have per-request limits and share a 60-second readiness/
forward deadline. Server output, request results and failure diagnostics are kept
under `privileged-port/`; cleanup has separate short bounds. A controller-command
deadline fails the phase without retry: it first allows a forwarded TERM to
complete, then uses forced local cleanup only as a failing last resort, with
explicit operation-receipt review guidance. It never acknowledges receipts.
Local regressions
exercise ordering, failed launch/readiness, wrong or missing forwarded responses
and cleanup uncertainty; they do not qualify a real forwarding route.
The root-descendant setup separately waits for its detached child's exec and
verifies UID 0, PID=PGID=SID and ignored HUP/TERM before the launcher exits.
Its receipt is printed and saved under `privileged-root-descendant/`; launch
failure or uncertainty fails the gate without retrying. The existing timed
container stop and post-restart absence assertions remain required: an escaped
root child must survive session cleanup for that test to be meaningful.
This smoke is not part of the combined gate.

The phases are:

- `smoke-space-lifecycle`: a declined mount change leaves config and host
  untouched, accepted mount add/remove is live when the command returns,
  mounted host-file creation modes, renamed/unlinked handles (post-unlink
  truncation asserted on Firecracker only), file fsync and
  atomic replacement, `space recreate` preserving declarations, Firecracker
  image preflight/replacement retaining native bytes and UUID, and
  `space destroy` removes accounts and declarations while preserving host workdirs;
- `smoke-image-ensure`: empty-cache base and composed image builds, latest
  Claude/Codex + latest Go availability, Codex's distro `bwrap` on the confined
  PATH, baked sandbox/home/npm ownership (checked
  with the entrypoint bypassed), cached-image selection, a small final ownership
  layer, apt/npm cache retention across base refresh and composed rebuild but absence from image
  layers, and default lifecycle autoupdate with an intentionally inaccessible `~/.npm`;
- `smoke-box-stop`: confined SIGTERM shutdown, exit 143, repeated stop/start,
  cached recreate without the 30-second escalation, and an unaffected peer;
- `smoke-memory`: a shared box allocates over 2 GiB, child/pool OOM completes
  within 30 seconds and leaves a peer alive, resource-only edits converge,
  boxes/BuildKit/RUN steps share the bounded pool, and builder death permits a
  later build without losing the VM.
  OOM probes have guest/host timeouts and collect pressure counters on failure.
  Requires at least 4 GiB and an updated Firecracker guest init;
- `smoke-features`: computed-policy round-trip, per-binary egress denial, PTY
  allocation denied by default but working after `fs.pty.allocate` is granted
  (which also proves the restarted sandbox accepts the PTY paths), and
  `box set` refusing an unknown capability and converging when repeated;
- `smoke-npm`: Node.js/npm/npx availability, sandbox-owned npm config/cache,
  registry dependency installation, an unchanged lockfile across offline
  `npm ci`, build/test scripts, local and downloaded npx commands, global
  installs without sudo, attributed curl denial and an in-sync box;
- `smoke-go`: the resolved `/usr/local/go/bin/go` behind `PATH`,
  `GOTOOLCHAIN=local` from Go's own `go.env`, a module dependency downloaded, checksum-verified and
  built from an empty module cache, a `go.mod` needing a newer Go refused by Go
  itself, attributed curl denial, no denial of the go binary and an in-sync box;
- `smoke-pip`: matching Python/pip commands, sandbox-owned user installs/cache,
  venv isolation and registry downloads, offline installation from hashed
  requirements, editable project installation with build isolation, project
  tests and console scripts, activation scoped to one exec, attributed curl
  denial and an in-sync box;
- `smoke-disk` (Firecracker): the autosized system disk equals half of the
  reported free space within 30–50 GiB, a shrink is refused, `space set --disk`
  applies only at the next recreate, and a data disk grow to 20 GiB (with the
  reflink copy, or `--no-backup` where the host cannot reflink) is refused for
  export while pending, finished by init at the next start, recorded at the
  stop, e2fsck-clean on the host when e2fsprogs is installed, sized 20 GiB in
  the guest and keeps a declaration byte for byte;
- `smoke-storage-gc`: active Firecracker host-storage guard, installed BuildKit
  budget, automatic startup and
  post-mutation image cleanup, confirmed cache-prune completion in the journal
  (or Firecracker VM log),
  retained declarations/aliases/immutable runtime
  identities, unlabelled created containers and stopped-box workdir data;
  pressure and cancellation paths use focused unit tests rather than filling
  a live VM disk;
- `smoke-agent-state`: account-owned state persistence across box replacement
  and VM replacement, native ext4 storage, same-name accounts isolated across
  two spaces, append/lock/shared-mapping/SQLite WAL through the native bind,
  synthetic history reopened and appended from fresh processes before and
  after box/VM replacement, and cleanup on account/space destruction;
- `smoke-attach-interrupt` (standalone, not in `smoke-live-vm`): interrupted
  attach clients never pause the space. In its own isolated space it starts
  sessions through `box attach` on a running container without a session and
  on a stopped box, then SIGTERMs/SIGKILLs the client once the attach's pending
  receipt shows authorized, unfinished setup, and closes a real output
  consumer at the setup-time session-exit notice and at the lock-wait notice
  (another box's `box recreate` prompt, fed from a FIFO and then declined,
  holds the space lock until the client has gone).
  One SIGTERM must end the client with the detached status 75 and its notice;
  every client wait is bounded. After each case another box mutation must
  succeed without acknowledgment, no receipt may be uncertain, at most one
  session may start, and a finite `box use` reattach joins it and shows its
  `session-ready` line. A case whose interruption lands elsewhere fails
  with a rerun hint. `ATTACH_INTERRUPT_EXPECT=fence` with a baseline
  `YOLOCTL` records the old fence and acknowledges it only inside the fixture.
  `ATTACH_INTERRUPT_CASES=after-handoff` (or `all`) instead sends one SIGTERM,
  SIGHUP or SIGINT to a `box attach` that already joined a running session,
  without a TTY and under a PTY: it must exit within 5 s (never with the
  before-handoff status 75), the session keeps running, no session starts and
  no receipt stays pending or uncertain; statuses and exit times go to
  `after-handoff.tsv`.
  Its `summary.tsv`, each case's finalized receipts and client output stay in
  `${SMOKE_ROOT}.attach-interrupt/` beside the removed fixture root;
- `smoke-box-attach`: the lifecycle auto-attach tail under a real PTY —
  create/copy/start/restart/recreate attach by default (generic-shell
  `bash -l`), session exit status propagates to the command's exit code, an
  externally terminated attach detaches (exit 0 + space-qualified hint) while
  the session survives in both managed and `--passthrough` modes. Managed
  reattachment restores a screen header evicted from a deliberately small
  byte ring; compatibility reattachment restores evicted keyboard negotiation.
  The fixture uses a short readiness prompt that fits its 128-byte ring;
  markers are constructed at execution so command echo cannot satisfy checks.
  Reattachment to the running fixture has a one-minute deadline.
  Both retain shell state, and
  `--no-attach` opts out. It checks the confined UID/GID against the host and that
  `box exec` gets a remote PTY with a terminal on stdin and stdout, and none
  (byte-exact stdout and stderr) once stdout is redirected. Its final phase
  drives a real TUI tab, standalone managed attach and `--passthrough` together.
  A raw-mode Python guest writes an independent audit of ordered input from
  each viewer, shared resize events, cursor-query replies and completion.
  The driver waits for the guest's flood acknowledgment before requesting
  resizes: stdin and geometry updates travel independently. Resize waits name
  the viewer/target size and retain control records in failure diagnostics.
  Changing output continues through managed detach/rejoin and an intentionally
  unread passthrough view; healthy viewers must keep making progress. A fresh
  passthrough view must rejoin, both standalone clients must return status 7,
  and the TUI must retain the tab's status. This finite transport scenario does
  not establish graphical parity or require an exact slow-client drop point.
  A subsequent `box-use.py` phase drives two structured agent frontends and a
  passthrough PTY against the same shell, checking visible runtime markers,
  peer activity from both kinds of viewer, unchanged geometry, detach survival
  and the remaining viewer's exit status. A bounded `box use ... batch --stdin`
  also joins that disposable shell while the other viewers stay attached,
  preserving session identity, a distinct client ID, ordered responses, screen
  metadata and a runtime-built shell marker after confirmed detach;
- `smoke-host-integration`: a generic shell without `fs.pty.allocate` exercises
  the confined browser helper with discarded stdio, exact PNG upload/path paste,
  shared-workdir reuse (including spaces), outside-workdir upload fallback,
  ordinary Ctrl+V fallback, visible browser confirmation/cancellation, automatic
  opening by default in one window, invalid schemes, cancellation on detach,
  repeated prompts after TUI reconnect, quiet replay, retained `BROWSER` after
  holder restart and exit status. Linux fixtures replace only
  `wl-paste` and `xdg-open`; the caller's clipboard/browser are untouched. This
  uses real frontends/controller/holder/OpenShell transport, but is not native
  desktop or provider image/auth acceptance. Non-Linux hosts explicitly skip
  this phase; native macOS clipboard/opener validation is separate;
- `smoke-controller-operator`: native declaration storage, the shared in-box CLI,
  nested exec bytes, concurrent host/helper writers, forbidden scopes and private
  paths, copied-helper denial, caller-local secret input, controller restart,
  operator revocation, graceful shutdown without fallback, offline rejection and
  persistence through VM recreation. Successful helper commands must not print
  internal transport errors.
  Skill checks verify readable mounted guidance and writable Gemini user-skill
  parents with read-only operator guidance, not actual agent discovery;
- `smoke-space-archive`: the portable backup/restore checks described below;
- `smoke-gemini`: latest Gemini installation and image settings, Node endpoint
  access and attributed curl denial, create-time API-key replacement, active
  native-import refusal, and synthetic Gemini state across box/VM replacement.
  `SMOKE_GEMINI_API_KEY` additionally opts into a real headless API and shell-tool
  request without the PTY grant. The default path needs no provider credentials.
  Google URL/code login, token refresh and interactive UI behavior (resize,
  cancellation, detach/reattach) still require manual validation in a disposable
  Gemini box. Record the installed CLI and host terminal versions when doing so.

- `smoke-agy`: latest native release installation, metadata-based status and
  update, launcher defaults before native execution, native executable egress
  with attributed curl denial, read-only operator guidance, active-import refusal
  and synthetic state across box/VM replacement. The CLI owns subsequent settings
  rewrites; this smoke does not establish an effective telemetry opt-out.
  Set `SMOKE_AGY_AUTH_FILE` to a disposable account's existing plaintext
  `~/.gemini/antigravity-cli/antigravity-oauth-token` to test real personal OAuth
  and shell tools in two fresh processes. The default path uses synthetic
  credentials and does not prove login or model/tool compatibility. First-login
  URL/code auth, token refresh, resize and detach/reattach need manual validation.
  Record the installed agy version and terminal configuration.

- `smoke-pi`: latest npm CLI and image-provided search tools, Node provider
  endpoint access and attributed curl denial, baked image identity/ownership,
  create-time secret replacement,
  active-import refusal and state across box/VM replacement. A deterministic
  local model fixture drives the real CLI's shell tool without a PTY grant,
  checks the operator skill listing in its model context and saves/resumes actual Pi
  sessions. This fixture does not establish real-provider acceptance.
  `SMOKE_PI_OPENAI_API_KEY` plus `SMOKE_PI_MODEL` opts into an actual OpenAI
  API/tool request. Browser OAuth, refresh and interactive terminal behavior
  need separate validation with a disposable account. Record Pi and terminal
  versions; the default smoke does not use real credentials.

- `smoke-opencode`: latest npm native executable and ripgrep, executable-scoped
  provider/registry HTTPS (including scoped npm URLs) with attributed curl/Node denials, native import refusal,
  create-time secret replacement, and account state across box/VM replacement.
  A local deterministic provider runs the real CLI, exercises shell tools without
  a PTY grant, requires the npm plugin SDK installation and a controlled plugin
  hook, checks operator skill discovery, and resumes its saved SQLite
  session after VM replacement. This needs no real credentials. Set
  `SMOKE_OPENCODE_OPENAI_API_KEY` and `SMOKE_OPENCODE_MODEL` (the bare OpenAI model
  ID) to additionally test a real provider request. OAuth login/refresh and
  interactive resize, cancellation and detach/reattach need separate validation.
  Record the installed OpenCode and terminal versions.

The separate `make smoke-airc` gate checks pinned AIRC image identity/home ownership,
that `/usr/local/share/airc/revision` equals the manifest's pinned revision,
skill installation, the packaged opt-in Codex `--wait-for-idle` adapter flags,
`subscribe --sink-timeout 0` and their recipe section (not a live Codex idle/busy
run), the absence of the removed Repartee client and `airc-box` launcher, a directly run `aircd` with two agent clients and the human account's
client, native named-client read replay, a confirmed send and reply named by a
shared `m:` reference that decodes to the observed msgid, works in `show` and
reappears in status after reconnect, human steering,
history after the human client reconnects, bounded shutdown, and a managed
session running `aircd` with detach/rejoin and persisted command revival after
restart. It then creates a separate server and two proxy-confined
participant boxes for executable-scoped denial, reconnect history, file sharing
and durable restart. It uses synthetic credentials, explicit plaintext on the
disposable VM bridge, and an explicit unconfined-network server; participant
boxes stay confined. Remote TLS deployment is outside this smoke. Rerunning with
the same `SMOKE_ROOT` updates the retained disposable controller to the current
build and resets the fixture boxes.

Like the managed room's server, its session runs `aircd` directly from a
`yolo.chat.skill` box. The nested driver's PTY grant is unused by that session.
`SMOKE_BACKEND=firecracker` selects the Firecracker fixture as usual.

Gemini, Pi and OpenCode provider-root HTTPS probes print each host and its HTTP
status. They check reachability plus an attributed OpenShell allowance, not
provider authentication or API health: unauthenticated roots can return 421,
redirects, challenges or rate limits. HTTP 2xx–4xx is accepted except proxy
authentication (407); transport errors and 5xx still fail. The separate
unauthorized-binary checks must also produce attributed denials. Registry
metadata/plugin checks continue to require successful responses and content.

Every phase that shares `smoke_init` runs with stdin at `/dev/null`, so a run
behaves the same from a terminal, an agent session, or CI: nothing prompts,
attaches, or allocates a PTY because of who launched it. `smoke-box-attach`
brings its own PTYs through `pty-run.py` and `attach-mixed.py` (Python on Linux/macOS).
The host-integration phase reuses that headless driver. The other phases still pass `--no-attach` on every
lifecycle call instead of relying on the non-terminal stdin to skip the attach.

The PTY harness is headless: it supplies scripted input, not the launching
terminal's query responses. The attach smoke sends CR for Enter; LF represents
Ctrl+J and can be encoded differently after keyboard negotiation is restored.
Capture files retain exact output bytes for
assertions. Stdout escapes controls and non-ASCII bytes as `\xNN` so renderer
queries and reporting modes cannot affect the terminal running the smoke, even
if a tested command hangs or is killed. Do not `cat` raw captures into a terminal;
inspect them with a byte-dump tool or an editor instead.

`make test-scripts` checks the PTY harness without Lima, including final output
written between a readiness sample and child exit, EOF/EIO, inherited slave
descriptors, timeout cleanup and safe diagnostic output. A real Bash/PTY test
also executes the smoke's shared setup/resume commands with a long initial
title/prompt and checks readiness retention, shell state and exit status.
The box-use driver has local tests for JSON framing, response IDs, malformed
output, EOF, deadlines, cleanup and finite-batch assertions; these do not
exercise a live VM.
The mixed-view harness transfers probe source, arguments and environment over
pipes through confined `box exec`, into a private temporary file. Only a short
launch command enters the PTY, avoiding large typeahead bursts across Bash's
Readline/line-discipline transitions. Local tests exercise that pipe installer
and real PTY launch, including a shell stopped in canonical mode, setup failure,
input/query/resize auditing, unread-output handling and child cleanup.
They force delayed flood input and a resize arriving during a previous size
sample, so those independent events cannot invalidate the audit;
negative controls reject lost, duplicated or reordered input and bad replies.
Host-integration harness tests exercise the same staged startup with long,
quoted/Unicode arguments and a retained browser path, its raw guest probe,
fragmented path paste, exact image hash, browser helper status and partial audit writes.
The browser flow sends explicit Escape reports: a bare Escape followed by a
letter would instead encode an Alt key and leave focus in the palette. The
Go frontend regression exercises both interpretations through the host reader
and a local PTY, including fragmented input.
These tests run in `make check` and Linux/macOS
CI. They do not replace `smoke-box-attach` on a host with Lima.

`make test-scripts` also checks the backend matrix's routing, isolation, failure
retention and exit status with stub commands, including explicit Lima selection
for the first-boot interruption fixture and selecting only archive phases.
Socket-path regressions bind real Unix sockets at the generated paths, including
restored instance names and Lima's temporary suffix, with a long caller `TMPDIR`.
These are harness regressions, not VM runtime acceptance.

Each phase target can also run independently and gets the same isolation. None
of them sees the caller's normal spaces, accounts, boxes, or context.

To run a smoke with an existing matching build, without rebuilding or
changing its bundle identity:

```sh
YOLOCTL="$PWD/bin/yoloctl" scripts/smoke/box-attach.sh
YOLOCTL="$PWD/bin/yoloctl" scripts/smoke/host-integration.sh
YOLOCTL="$PWD/bin/yoloctl" scripts/smoke/npm.sh
YOLOCTL="$PWD/bin/yoloctl" scripts/smoke/go-modules.sh
YOLOCTL="$PWD/bin/yoloctl" scripts/smoke/pip.sh
```

`SMOKE_PRESERVE_SPACE=1` retains a standalone run's isolated space and artifacts
on both success and failure. The final phase stores raw captures and `audit.jsonl`
under the printed smoke root's `attach-out/mixed/` directory. Its Python driver
expects that disposable fixture; do not aim it at an existing working session.

Claude, Codex, Gemini, OpenCode and Pi features install npm `latest`; agy installs the latest
official native release. The smoke harness does not pin agent versions;
the npm and pip environment smokes pin their fixture dependencies only.
The image-selection setup passes
`--no-autoupdate` so it can exercise the cached-image branch independently,
then forces version drift and verifies the default lifecycle autoupdate through
the dedicated maintenance cache.

`make smoke-account-auth` remains separate because it requires a test
secret (a dummy value is enough; it checks delivery, never provider access) in `SMOKE_ANTHROPIC_API_KEY`.

`make smoke-space-archive` checks private archive publication, native auth bytes,
portable Codex daemon links, omitted runtime sockets/helpers/PIDs, unchanged
source state, rejected-link diagnostics, new disk identity, host-workdir
remapping, independent duplicate state, persistence through recreation and
returning a stopped source VM to its stopped state.
It is also included in the combined gate. It uses synthetic credentials.

To test a separate build without replacing the default binaries, build once
and invoke the scripts with an explicit executable:

```sh
make BIN_DIR=bin/candidate build
SMOKE_KEEP_ON_FAILURE=1 YOLOCTL="$PWD/bin/candidate/yoloctl" scripts/smoke/live-vm.sh
YOLOCTL="$PWD/bin/candidate/yoloctl" scripts/smoke/space-archive.sh
```

These scripts require a real host VM backend. Unit tests, simulated transport
and cross-compilation do not establish that a live gate passed. Never use the
VM/account hosting an active coding session as the test fixture.

For host-storage changes, also run the real process-suspension regression on
the Linux host (not inside an OpenShell box). It signals only a disposable child
and never fills a filesystem or touches a VM:

```sh
YOLOCTL_TEST_REQUIRE_STORAGE_SIGNALS=1 go test ./internal/firecracker -run HostStorage -count=1
```

The required-signals setting turns a sandbox's denied pidfd call into a failure;
ordinary confined checks explicitly skip that one process test. Then run
`SMOKE_BACKEND=firecracker make smoke-storage-gc` for the isolated native gate.

For the chat Participants lookup under a held space lock, run the opt-in live
Go test from the disposable space's host (not inside a box), in that space's
isolated `YOLOCTL_HOME`. Use the inspected root printed by that smoke run.
The space needs a running managed chat room with a
participant whose box runs (`_MEMBER`) and another box (`_HOLDER`). The test
binary is linked with the space's controller bundle ID and must be built from
the checkout `YOLOCTL` was built from; it re-executes itself for the VM
transport:

```sh
SMOKE_ROOT="/tmp/yoloctl-smoke.EXAMPLE"
BUNDLE=$(sed -n 's/^bundle: //p' "$SMOKE_ROOT/home/spaces/$SPACE/space.yaml")
YOLOCTL_HOME="$SMOKE_ROOT/home" YOLOCTL="$PWD/bin/yoloctl" YOLOCTL_TEST_LIVE_CHAT_SPACE="$SPACE" \
  YOLOCTL_TEST_LIVE_CHAT_MEMBER=member-box YOLOCTL_TEST_LIVE_CHAT_HOLDER=other-box \
  go test -count=1 -v -run TestLiveChatAttachTargetAnswersWhileSpaceLockIsHeld \
  -ldflags "-X github.com/leogr/yoloctl/internal/controller.BuildID=$BUNDLE" ./cmd/yoloctl
```

It holds the lock with the holder's `box recreate` prompt (declined afterwards,
never killed), runs the click's lookup through the chat view's own path and
deadline, and requires a correct answer in under 10 s, the holder still pending
and no pending or uncertain receipt left. Without the variables it skips.

To remove a retained fixture after inspection, use its printed root explicitly:

```sh
SMOKE_ROOT="/tmp/yoloctl-smoke.EXAMPLE"
YOLOCTL_HOME="$SMOKE_ROOT/home" \
  ./bin/candidate/yoloctl space destroy smoke-live-vm --force
```

Remove that temporary directory only after space destruction succeeds. Keep the
build used for that fixture; a rebuilt CLI may select a different controller
bundle and requires the explicit update workflow.

### macOS test launch diagnostics

`internal/tui/term/pty_output_test.go` keeps all three fast-exit output cases
strict. A launch failure logs its unwrapped errno and operation; it is not
retried or skipped. For a bounded comparison of ordinary exec, a new session
and a controlling PTY, run the opt-in test with the actual selected Go toolchain:

```sh
YOLOCTL_TEST_PTY_DIAGNOSTICS=1 go test -v -count=1 -run '^TestPTYStartDiagnostics$' ./internal/tui/term
```

Record the Go executable and test binary's Mach-O architecture when comparing
native Apple Silicon with amd64 under Rosetta. `GOARCH=arm64` alone does not
prove that the Go tool itself ran natively. The diagnostic uses only fixed,
small-output disposable commands and records no inherited environment values.
It drains controlling-PTY output concurrently and closes the pollable master
before waiting for cancellation cleanup, so an unread terminal cannot stall
the diagnostic itself. The strict output test keeps its separate byte assertions.
Darwin's Go fork/exec error pipe merges session, controlling-TTY and exec errors;
`cmd.Start` EPERM alone leaves the exact child syscall unknown. A successful
comparison run is a no-reproduction result, not proof that Rosetta caused a
previous failure. Preserve the first failure and run the ordinary parallel full
Go suite as well as isolated repetitions; do not substitute `-p1` for that gate.

### Falco audit host qualification

Run only from the host against a separately owned disposable space. Do not use
the space hosting this development session. Local rule/unit tests do not prove
modern-eBPF loading or workload identity.

Every host command must explicitly select the fixture home with
`YOLOCTL_HOME="$SMOKE_ROOT/home"`; never inherit a different home. Verify
SMOKE_ROOT is the owned test fixture before using these examples.

For Lima, start the disposable space with this controller build, then run
`yoloctl audit enable --space NAME`. Check `audit status --space NAME --json`
for enabled desired state, active service, pinned tool version and a loaded
modern-eBPF driver. Failed status includes the phase, process/timeout error and
a bounded escaped stderr prefix; it does not infer a kernel problem from every
Falco failure. Validation with `-V` alone does not open the capture engine or
prove event-schema compatibility. In the VM, run an owned executable named
`yolo-audit-test` to exercise the controlled rule. Read `audit logs --space
NAME --json`, then `audit export --space NAME --output NEW.jsonl`; check the
finite interval/count and explicit drops/metadata/retention gaps. Confirm a
second export to the same file is refused. Disable auditing; prove the service
and probe are stopped and retained export still works. Verify boxes were not
restarted. Measure idle/build overhead and drops; no SLA is asserted.

For that controlled rule, in the disposable VM (or a writable directory of a
disposable box), copy an ELF executable rather than using a shell script:

```sh
audit_probe_dir=$(mktemp -d /tmp/yoloctl-audit-test-XXXXXXXX)
cp /usr/bin/true "$audit_probe_dir/yolo-audit-test"
"$audit_probe_dir/yolo-audit-test"
```

A direct VM execution has no box metadata and displays `(vm)`. A box
execution needs live container/plugin metadata for enrichment. Check that
pretty output shows the pinned rule message, available process/user/container
and file fields without `<NA>` clutter or a repeated raw field dump; `--verbose`
adds raw fields, JSON always retains them. Check narrow-terminal wrapping and
four/eight-space message/field indentation. Old records cannot recover newly
added fields. In follow mode,
the initial summary appears once; idle sliding-window polls and new events do
not repeat it unless loss/gap state changes. Verify idle TUI selection is stable,
selected messages/fields are readable, and terminal colours become plain when
piped or with `NO_COLOR=1`. These remain host checks, not fixture-test proof.

For metadata-plugin qualification, confirm the pinned `yolo` and `container`
plugins loaded. The controller publishes a private snapshot with generation metadata;
read/exported events must show the selected `yolo.*` fields. Stop publication in
the owned fixture, wait beyond the 15-second freshness bound and prove events
remain visible with unavailable metadata. Check missing/ambiguous IDs and
container recreation/name reuse. No raw Env/argv may appear in the snapshot,
logs or export. Repeat on native Linux amd64 and arm64; cross-building and a
synthetic ABI fixture are not native Falco event evidence.

For rules/labels qualification, create a fresh disposable box with this build.
Verify the native system containerd/default-namespace identity and the selected
`yoloctl.audit.*` labels, then compare exported box/space/account/agent/runtime
and network fields to the actual launch. Missing legacy labels and delayed
metadata must produce an unlabelled event, not silence. Run the controlled test
inside both confined and declared unrestricted-network boxes. Exercise ordinary
shell, git, compiler and permitted debugger workloads; do not assert that every
alert is malicious or that labels prove the triggering PID was confined.

| Rule family | Disposable positive case | Negative/noise control |
|---|---|---|
| Runtime/admin socket | Attempt a listed admin socket; inspect success/refusal | Ordinary operator endpoint traffic |
| Infrastructure | Attempt a write to selected infrastructure | Controller deployment, audit rotation and verified OpenShell setup |
| Kernel instrumentation | Attempt a workload BPF/module syscall | The supervised VM-root Falco probe load |
| Namespace | Attempt a workload namespace/mount syscall | Verified OpenShell launch paths/context |
| Process inspection | Unexpected cross-process inspection | Configured debugger executable in known confined context |
| Protected file | Denied open of a listed sensitive file | Ordinary allowed project reads |
| Persistence | Change selected unit/cron/key/loader paths, including real symlink locations and rename/hard-link targets | Host package maintenance and containerd/BuildKit image-layer symlinks pointing at protected VM paths |
| Anonymous executable | Explicit opt-in test only; rule starts disabled | Compiler/package temporary executables |

Repeat image-build/unpack controls with containerd, BuildKit, nerdctl and shims;
verify their genuine writes to protected VM paths still alert. After explicit
updated enable, compare a box event with its native full container ID and labels
on both Lima systemd scopes and Firecracker cgroupfs. A `(vm)` label reflects
Falco's host report and is not proof that container recognition succeeded.

These are later host acceptance cases, not actions for the active development
space. Record actual metadata gaps, false positives and missing syscall evidence;
a rule-validation or synthetic event fixture PASS does not establish these hits.



#### Escape-family qualification

Keep this campaign separate from ordinary audit enable/event checks. On the
final candidate, only **Yolo UID set to root** and **Yolo audit escape test**
ship enabled; all six other escape candidates remain disabled. Do not enable
candidates in the active team space or infer qualification from `-V`/nodriver.
Run the non-skipped archive test on the authorized privileged executor with
both `FALCO_TEST_ARCHIVES` (qualified official tarballs) and
`FALCO_TEST_ENGINE=modern_ebpf`; save the actual result and architecture.

In one disposable confined box, copy an ELF executable to the distinct
13-byte basename `yolo-esc-test` and run it through the ordinary confined
launch/session path. Require its selection-control event. The same executable
run through outer maintenance (`nerdctl exec --user sandbox`) is the negative
leg and must not emit that control. A control hit proves neither an escape nor
coverage of another predicate. Record kernel, Falco/OpenShell versions, labels,
collector health and available loss counters before/after every window.

With the fixture's managed collector acknowledged stopped, an authorized
read-only diagnostic capture should record connect/exec fields `fd.cip`,
`fd.rip`, `fd.rport`, `evt.res`, `proc.aexepath[1]` through `[3]`,
`proc.pvpid`, `user.uid`, UID arguments and their actual JSON types. Exercise
ordinary agent traffic to different remotes, localhost and Unix sockets,
outer-maintenance traffic, unconfined networking and box startup/root helpers.
For the UID predicate, use `evt.args contains "uid=0("` on only successful
setuid/setreuid/setresuid events. Resolved `evt.arg.*` are names/placeholders;
the pinned engine rejects typed raw-UID comparisons. In this diagnostic only,
omit the ancestry macro and print `%evt.args` once to inspect its numeric and
resolved forms; keep aggregate arguments out of ordinary retained events. From the VM, run a root no-op in the owned disposable container:

```sh
nerdctl exec --user root "$FIXTURE_CONTAINER" python3 -c 'import os; os.setuid(0)'
```

Require a successful diagnostic event containing `uid=0(`, independently of
whether its resolved name is present or unavailable. This is outside-sandbox
predicate qualification, not an escape or proof of the production selector;
keep the confined control/outer-maintenance negative leg separate. Do not widen
workload grants or substitute `evt.arg.uid = root`. No absent tuple may be treated as an escape. SUCCESS and EINPROGRESS must be
reported separately; source-address anomalies do not prove proxy bypass.
Keep the network candidate disabled until that evidence is reviewed; root exec
and host-classified exec remain disabled regardless of this diagnostic.

The benign window is at least 20 minutes on a disposable confined PTY-capable
box with writable mounts under `/srv` and `/usr/local/bin/project`: clone,
build/test, npm install, agent work, agent update, box restart and new session.
Require no enabled escape detections (count control events separately), healthy
collection and unchanged loss counters, otherwise mark inconclusive. Record
expected broader-prefix/device candidate activity rather than treating it as a
violated invariant. Check PTY aliases, descriptor aliases, directory/O_PATH
opens and `/dev/shm`. A writable single-file PATH-child bind remains a known
counterexample to the PATH candidate. Any later candidate enablement requires
the plan's evidence and a separately reviewed change. Lima arm64 and
Firecracker amd64 require separate native baselines; one does not qualify the
other. No exploit, grant widening or automatic remediation is needed.

For Firecracker, begin with a separately owned old-image fixture. Audit enable
must refuse before installing tools or changing desired state, with the explicit
image-update hint. From the host run
`YOLOCTL_HOME="$SMOKE_ROOT/home" yoloctl space recreate NAME --update-vm-image`
using the candidate bundle. This discards sessions/ephemeral workdirs and
preserves native data/accounts/declarations; boxes need explicit starts. Verify
the same native disk UUID and selected account/declaration bytes afterward.
A controller-only update cannot change the kernel or PID-1.

On the updated native amd64/KVM fixture, verify `/sys/kernel/btf/vmlinux`, then
repeat the Lima enable/event/export/disable and metadata/rule cases above.
Confirm a loaded modern-eBPF probe, fixed PID-1 service ownership, acknowledged
collector/descendant teardown, controller/VM restart reconciliation and no box
restart on a toggle. Do not infer driver support from rule validation, BTF
section inspection or a simulated backend. Repeat the Lima checks against
this final candidate. Audit logs/tools are omitted from ordinary space backup;
an import must have auditing disabled.
