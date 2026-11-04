# Image build progress

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] The preparation driver sends a notice naming the desired image and a
validated box name, when applicable, before resolving base/dependency images.
Managed-base and composed preparation both use this path, as do manual builds.
The controller writes an immediate preparing line through ordinary framed stderr;
quiet preparation receives a phase/elapsed notice after two seconds without
output and every two seconds thereafter. These notices grant no effect
permission and never retry work. [1](#source-1) [2](#source-2) [6](#source-6)

[FACT] Every BuildKit Solve receives a status channel drained by the pinned
BuildKit plain renderer. It reports vertices, cache hits and build stdout/stderr;
collected warnings are printed with their details. The renderer drains until
Solve closes the channel even on error/cancellation. Solve's original response
and error still determine existing private-output validation and failure or
uncertain-outcome classification. Actual build diagnostics can include whatever
the build prints. No durable build-log store is added. [3](#source-3) [7](#source-7)

[FACT] Each job retains at most 1 MiB of recent output. Registering a joiner
serializes its phase/replay and subscription with live publication, preserving
order across the handoff. Earlier omitted bytes are explicit. Every requester
has an independent 1 MiB queue and one sender; producers only enqueue memory,
never write the client under a job/scheduler lock. Oversized output is chunked
into at most 32 KiB frames. Slow-client overflow reports skipped bytes without
blocking another requester or Solve. Request connection cancellation closes its
transport and releases its sender. [2](#source-2) [4](#source-4) [8](#source-8)

[FACT] The driver drains progress outside native guards before re-entering
apply; an error also drains before the final response.
Progress never travels through machine stdout, image checkpoint replies or the
managed view/snapshot channel. Cancelling a leader removes only that interest;
followers retain their own log subscription and shared solve. Last-interest
abandonment still uses the existing private-output/capacity release contract.
[2](#source-2) [4](#source-4) [8](#source-8)

[FACT] When the CLI makes an interactive terminal raw, it supplies CR before
bare LF in controller stderr directed to a terminal, keeping progress lines at
column zero. Existing CRLF, including a pair split across frames, and standalone
CR updates are preserved. Redirected stderr, session stdout and the private
view bypass this conversion. Raw mode and its restoration retain their existing
lifetime. Real PTY regressions check the raw-mode call path, captured session
bytes, restored termios and column placement using the pinned VT core; they do
not establish live VM behavior. [10](#source-10) [11](#source-11) [12](#source-12)

[FACT] Regression fixtures cover fake-time quiet feedback, ordered replay/live
handoff, overflow and slow-client isolation, cancellation, concurrent join/close,
leader departure, real renderer statuses/warnings and framed stderr before final
success/failure beside unchanged stdout. They simulate the backend; they are not
proof of live BuildKit, Firecracker or Lima behavior. [5](#source-5) [9](#source-9)

## Sources

1. <a id="source-1"></a> [Preparation driver](../../cmd/yoloctl/controller_images.go#L164) — `imageWarmState.prepare`, `warmBoxBeforeEffects`, `warmingPath`.
2. <a id="source-2"></a> [Request transport](../../internal/controller/server.go#L755) — `ServeConn`, private `preparing` action, acquire callback, `prepared` drain and finalization; connection cancellation at L341.
3. <a id="source-3"></a> [Solve renderer](../../internal/controller/image_solve_progress.go#L1) — `solveImageWithProgress`.
4. <a id="source-4"></a> [Bounded fanout](../../internal/controller/image_progress.go#L1) — `imageProgress`, `imageBuildLog`.
5. <a id="source-5"></a> [Progress regressions](../../internal/controller/image_progress_test.go#L1) — scheduler/fanout/renderer and wire fixtures.
6. <a id="source-6"></a> [Preparation regressions](../../cmd/yoloctl/controller_images_test.go#L1) — initial resolution notice and manual/base preparation.
7. <a id="source-7"></a> [Native backend](../../internal/controller/image_backend.go#L276) — `Build`, `classifyImageSolveError`.
8. <a id="source-8"></a> [Jobs](../../internal/controller/image_jobs.go#L204) — `acquireWithProgress`, `run`, unchanged `leave` and `abandonLocked`.
9. <a id="source-9"></a> [View regression](../../cmd/yoloctl/controller_view_prepare_test.go#L1) — inherited view and queued input across preparation.
10. <a id="source-10"></a> [Interactive client](../../cmd/yoloctl/controller_client.go#L384-L467) — `callControllerTerminal`, raw mode, stderr-only adapter and unmodified view/stdout routing.
11. <a id="source-11"></a> [Raw terminal diagnostics](../../cmd/yoloctl/controller_stderr.go#L8-L64) — `rawTerminalStderr.Write`, newline and partial-write handling.
12. <a id="source-12"></a> [PTY regression](../../cmd/yoloctl/controller_stderr_test.go#L27-L228) — `TestControllerTerminalProgressColumns`, redirected stderr and writer failures.
