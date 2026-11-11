# Image build progress

Verification: `85f67be08d8bceefb03db745cad123ea5bbade49` and accompanying changes.

[FACT] The preparation driver sends a notice naming the desired image, a
validated box name and, for a composed image, its first 16 feature names with
a count of the others (a name that is not plain text is counted, never a reason
to refuse), when applicable, before resolving base/dependency images.
Managed-base and composed preparation both use this path, as do manual builds.
A request's first notice starts its root step `Image for box X (features: …)`;
every job step it follows is re-parented under that root in its own queue,
join/wait notices are root detail. The request's end decides `Image for box X
ready (features: …)` (else failed with a job's error, or uncertain), sent after
every owed replay; an overflow resends the root first: one collapsed line. Quiet preparation receives a Debug `Still preparing image … (elapsed)` record
after two seconds without output and every two seconds thereafter, shown with
`--log-level debug`. These records grant no effect permission and never retry work.
[1](#source-1) [2](#source-2) [6](#source-6)

[FACT] Each image job reports steps through its own wire-format Logger: a
`Building`/`Pulling`/`Retaining
local image <image> (features: …)`, titled from the notice of the request that
started the job (else its short ID, kept as the `job` field) (actor `builder`, executor `buildkit` or `containerd`), each
ended with the job's result: ok, a known build failure, or an uncertain
interruption in the existing error wording. Every BuildKit Solve's status
channel is drained until Solve closes it, even on error/cancellation: a vertex
becomes a child step when it starts (cached ones end with `cached=true`, errors
fail, cancellations are skipped), vertex logs become its detail lines (CR
redraws split into lines; a status's logs are applied before its completions,
and later logs still join the ended step's detail), and warnings become warn
records. Vertices still
open when Solve returns end as done or, after a failure, skipped. Solve's
original response and error still decide validation and failure or uncertain
classification. No durable build-log store is added. [3](#source-3)
[7](#source-7) [8](#source-8)

[FACT] Each job keeps a step Tracker (at most 200 steps, 1 MiB of detail) and
its last 32 records that belong to no step. Registering a joiner replays those
records, the Tracker snapshot and a step-less snapshot end marker under the
same lock as live publication, so the follower's own Tracker reproduces the
job's steps; states only move forward. Every requester has one sender and an
independent queue of encoded records, at most 1 MiB: lossy detail records are
dropped first (down to 3/4, with a Debug `skipped N bytes` record). If other
records still exceed it, the queue is emptied and each followed job's live
records are discarded until the sender, with an empty queue, captures that
job's replay under the job's lock (one snapshot/live cut) and queues it within
the remaining room: step states and markers always (compacted to 256, then 32
bytes per title/err/reason without fields when needed, else captured later
with an empty queue), then step-less records and tail lines newest first while
they fit, left-out lines added to the step's omitted bytes. A notice
counts what was skipped. The current state of every tracked step (within the
200-step Tracker) is delivered; superseded transitions and older step-less
records may not be. A job is released once its records are delivered.
Producers only enqueue memory, never write the
client under a job/scheduler lock. Request connection cancellation
closes its transport and releases its sender. [2](#source-2) [4](#source-4)
[8](#source-8)

[FACT] The driver drains progress outside native guards before re-entering
apply; an error also drains before the final response.
Progress never travels through machine stdout, image checkpoint replies or the
managed view/snapshot channel. Cancelling a leader removes only that interest;
followers retain their own log subscription and shared solve. Last-interest
abandonment still uses the existing private-output/capacity release contract.
[2](#source-2) [4](#source-4) [8](#source-8)

[FACT] On the host, relayed records go to the process Logger when the
command's stderr is the process stderr (an interactive terminal shows the
steps block), otherwise as plain lines to the caller's stderr writer. When the
CLI makes an interactive terminal raw, it supplies CR before bare LF in
controller stderr and in the Logger's lines (the steps block writes CRLF
itself), keeping lines at column zero. Existing CRLF, including a pair split
across frames, and standalone CR updates are preserved. Redirected stderr,
session stdout and the private view bypass this conversion. Real PTY
regressions check the raw-mode stderr path using the pinned VT core; they do
not establish live VM behavior. [10](#source-10) [11](#source-11)
[12](#source-12)

[FACT] Regression fixtures cover fake-time quiet feedback, ordered snapshot/live
handoff, transitions kept on detail overflow, bounded structural overflow and
replay (detail-rich and long-title jobs), one snapshot/live cut per job (also with two jobs publishing
concurrently), output at and after a vertex's completion, slow-client isolation, cancellation,
concurrent join/close, leader departure, BuildKit statuses/logs/warnings as
steps, and log frames before final success/failure beside unchanged stdout.
They simulate the backend; they are not proof of live BuildKit, Firecracker or
Lima behavior. [5](#source-5) [9](#source-9)

## Sources

1. <a id="source-1"></a> [Preparation driver](../../cmd/yoloctl/controller_images.go#L164) — `imageWarmState.prepare`, `warmBoxBeforeEffects`, `warmingPath`.
2. <a id="source-2"></a> [Request transport](../../internal/controller/server.go#L361) — `ServeConn`, private `preparing` action (`log` frame sender), acquire callback, `prepared` drain and finalization.
3. <a id="source-3"></a> [Solve steps](../../internal/controller/image_solve_progress.go#L1) — `solveImageWithProgress`, `solveSteps.apply`, `detailLines`, `finish`.
4. <a id="source-4"></a> [Bounded fanout](../../internal/controller/image_progress.go#L1) — `imageProgress.fromJob`/`pushLocked`/`resyncOne`/`next`/`run`, `encodeReplay`, `imageBuildLog.Logger`/`publish`/`replayLocked`/`subscribe`.
5. <a id="source-5"></a> [Progress regressions](../../internal/controller/image_progress_test.go#L1) — scheduler/fanout/steps and wire fixtures.
6. <a id="source-6"></a> [Preparation regressions](../../cmd/yoloctl/controller_images_test.go#L1) — initial resolution notice and manual/base preparation.
7. <a id="source-7"></a> [Native backend](../../internal/controller/image_backend.go#L211) — `Build`, `classifyImageSolveError`.
8. <a id="source-8"></a> [Jobs](../../internal/controller/image_jobs.go#L213) — `acquireWithProgress`, `run` (job steps at L268 and L587), `endSteps`, unchanged `leave` and `abandonLocked`.
9. <a id="source-9"></a> [View regression](../../cmd/yoloctl/controller_view_prepare_test.go#L1) — inherited view and queued input across preparation.
10. <a id="source-10"></a> [Interactive client](../../cmd/yoloctl/controller_client.go#L344-L460) — `callControllerTerminal`, raw mode, stderr adapter, Logger routing and unmodified view/stdout routing.
11. <a id="source-11"></a> [Raw terminal diagnostics](../../cmd/yoloctl/controller_stderr.go#L8-L64) — `rawTerminalStderr.Write`, newline and partial-write handling.
12. <a id="source-12"></a> [Host record routing](../../internal/controller/client.go#L45) — `clientLogStreams`; steps block in pkg/prettylog/stepview.
