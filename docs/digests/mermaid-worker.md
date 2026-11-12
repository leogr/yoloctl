# Mermaid renderer worker

Verification: `b868ea74f8d99085022bed5f53ef34ecfe3ac740` and accompanying changes.

[FACT] `yoloctl __mermaid-worker` is dispatched before any CLI, controller or
terminal initialization. Outside the host CLI context (a controller context,
its context file, or an agent box) it exits 2 before reading anything;
otherwise it serves length-framed JSON requests on stdin until it closes. One wazero runtime with a 64 MiB guest limit compiles the embedded
artifact once; each request runs in a fresh module instance with a 2 s
deadline, no environment, preopens or host handles, stdout bounded to 256 KiB
and stderr to 4 KiB. The worker's Go heap targets 256 MiB (soft). Requests are
checked strictly (protocol version, artifact hash, width 1–4096, width method,
nonempty UTF-8 source without controls, in a request frame of at most 1 MiB,
a transport bound). [1](#source-1) [2](#source-2)

[FACT] Outcomes are honest: the deadline is `timeout`; a trap or abort,
including at the memory limit, is `renderer failed`; exit 3 is a parse error
and exit 4 too wide with the needed columns. A drawing is accepted only if it is
UTF-8 without controls (TAB and CR included), at most 120 rows (else
`too tall`) and every row fits the requested width under the view's width
method. A dense flowchart (K25) ends as a timeout at the real limits and as a
renderer failure under a 16 MiB test ceiling. [2](#source-2) [3](#source-3)
[5](#source-5)

[FACT] `Client` starts one worker lazily (minimal environment, own process
group, 1 s wait delay, discarded stderr), only on the host. Requests are
serialized; a guard of 4 s from enqueue covers queue wait, worker startup and
the reply. A start already running when its request gives up still becomes
the client's worker (ended by `Close`); one not yet begun when its request gave
up, or requested before a `Close`, never begins. A
request still queued at its guard is `renderer busy`; a worker that goes away
or does not answer in time is killed (process group) and reported `renderer
unavailable`; a reply that breaks the framing, JSON, protocol or drawing checks
is an internal error and also replaces the worker. A crashed or killed worker restarts at most once per 30 s.
A caller whose context ends while the worker renders gets `renderer busy` at
once; that worker is killed too, but as no failure: no restart throttle follows.
No diagram is retried automatically. [4](#source-4) [5](#source-5)

[FACT] A per-process LRU (64 entries, 4 MiB of drawing text, never on disk)
keys sha256 over the artifact hash, width method, source and width. It keeps
drawings, too wide/tall, parse errors and, as a no-retry policy, timeouts and
renderer failures; busy, unavailable and internal outcomes are never cached.
Under `-race` the tests that assert the real 2 s/4 s limits are skipped.
[4](#source-4) [5](#source-5)

## Sources

1. <a id="source-1"></a> [Dispatch](../../cmd/yoloctl/main.go#L53) — early `mermaid.WorkerArgument`; internal/mermaid/worker_main.go `WorkerMain`.
2. <a id="source-2"></a> [Worker](../../internal/mermaid/worker.go#L31) — `runWorker`, `render` (L80).
3. <a id="source-3"></a> [Protocol and checks](../../internal/mermaid/protocol.go#L177) — `verifyDrawing`, `Result.valid`, `request.valid`, frames.
4. <a id="source-4"></a> [Client and cache](../../internal/mermaid/client.go#L147) — `Render`, `exchange`, `startLocked` (L82), `onHost`; cache.go `resultCache` (L36).
5. <a id="source-5"></a> [Regressions](../../internal/mermaid/client_test.go#L1) — client cache/timeout/busy/hung-worker/protocol/host/too-tall/LRU and cancel-without-throttle tests; worker_test.go controlled trap, real-limit timeout, deadline and output bounds.
