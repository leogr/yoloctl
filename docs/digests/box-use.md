# Collaborative structured terminal frontend

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] `box attach --box-use` (also root `attach --box-use`) selects a frontend
mutually exclusive with `--passthrough`. It reuses controller-authorized attach,
ensure semantics and private managed views; command stdout/stderr stay
diagnostics. It requests no PTY, resize, measured colors, browser or clipboard. [1](#source-1)
[2](#source-2) [9](#source-9)

[FACT] `box use BOX ACTION` delegates to that public attach path and defaults
to `--no-autoupdate`. It validates finite local requests before attach,
preserves ready/session/client and response metadata, waits for each result,
stops on rejection and drains detach/receipt without replay. Batches allow 32
requests/256 KiB, unique IDs, no blank lines and an optional final detach.
Text/key validation reuses the frontend's checks. Regular stdin files, such as
larger Bash heredocs, cannot join epoll and are read directly; pipes and
terminals keep cancellable reads. A descriptor that `os.SameFile` matches
with the `os.DevNull` character device is read directly as EOF, whatever its
name; other devices stay cancellable. Finite empty input keeps its local error.
[2](#source-2) [14](#source-14) [15](#source-15) [18](#source-18) [19](#source-19)
Actions/keys/modifiers and box names complete through Cobra. Explicit stdin
forms open a palette tab. [14](#source-14) [17](#source-17)

[FACT] Finite waits without `since` use the last observation from this attachment
or emit a baseline screen first. Explicit batch `since` retains wire semantics;
revisions cannot be reused across attachments. Timeouts still return the current
screen. The overall deadline defaults to two minutes (1s–10m), including startup
but excluding bounded receipt cleanup, whose timeout preserves per-action
results. [14](#source-14) [15](#source-15) [16](#source-16) [19](#source-19)

[FACT] The interface is JSON Lines over persistent stdin/stdout pipes. An initial
`ready` record identifies interface version 1, the session incarnation and this
attachment. Requests require `id` and `op`. IDs correlate replies; they do not
deduplicate actions. Unknown, duplicate, null and operation-inapplicable fields
are rejected. Request lines are bounded below 64 KiB. Operations run in input
order, including waits; EOF detaches after queued requests. [3](#source-3)

| Operation | Request fields and result |
|---|---|
| `screen` | Optional `since`; screen text, cursor, geometry, title, alternate-screen/hold state, revision and input activity. |
| `wait` | `since`, optional `timeout_ms` (0–60000, default 30000), `settle_ms` (0–1000, default 100); observation after a settled change, or `timed_out` at the deadline. |
| `text` | `text`: 1–16384 UTF-8 bytes; inserts without appending Enter. |
| `key` | Native key name such as `enter`, `arrow_up`, `key_c`; optional `modifiers` array containing `shift`, `ctrl`, `alt`, `super`. |
| `history` | `offset` back from the newest retained line; `limit` 1–200 (default 100); total/start/lines and truncation. |
| `detach` | Ends this attachment, preserving the session. |

[FACT] Screens contain at most the last 200 grid rows and 32 KiB of text,
with explicit truncation. History shares those bounds; the replica caps visible
history at 10000 lines without changing canonical retention.
Coordinates are zero-based; `start_row` is the first included row.
A matching `since` and last observation baseline return changed rows; stale
baselines, screen switches, resizes or truncation return a full bounded window.
Unchanged text/cursor/metadata does not advance the revision; intermediate
redraws remain local. Synchronized output preserves the previous screen until
release. A timeout or quiet screen is not semantic completion.
[3](#source-3) [4](#source-4)

[FACT] Text rejects control characters except line breaks and tabs, which require
bracketed paste mode. Keys encode press/release using restored native modes.
All input follows the existing ordered command-stdin route. `sent` waits until
activity reports this attachment's cumulative queued bytes equal to its writes;
peer counts, sequences and partial progress cannot satisfy it, so detach follows
holder acceptance. That is not reading, execution or durability across holder
death. Write errors, cancellation, refusal or disconnect first report
`input_uncertain` and end the frontend without replay. Reads and later input are
not atomic against other participants. [3](#source-3) [4](#source-4) [7](#source-7)

[FACT] Private session protocol 6 carries ephemeral attachment IDs and ordered
accepted-input activity: a packet sequence plus the source's cumulative queued
bytes, without key contents. Every attachment remains
interactive. Managed observers receive activity from managed and passthrough
participants; compatibility viewers receive only their terminal stream. The
frontend reports the latest sequence and most recent peer revision/source,
without ownership locks, priorities or inferred intent. Query replies are not
participant input. Exact bundle/core/protocol checks require replacing holders
from other builds. [5](#source-5) [6](#source-6)
[10](#source-10) [11](#source-11)

[FACT] Deliberate detach signals only the controller worker, drains private
output to obtain its final receipt and leaves the holder alive. Known exit
retains final screen/status; missing receipts stay explicit unknown outcomes.
Killing the client or closing its output during setup never pauses the space;
see [owned attach setup](attach-setup.md). Local tests cover validation, rejection, cancellation, provenance,
ordered input, holder acceptance, peer activity, redraws, bounds, keys, exit and
detach. The attach smoke includes a
passthrough PTY and two box-use clients; its existence proves neither live-VM
success nor graphical parity. [2](#source-2) [7](#source-7)
[8](#source-8) [12](#source-12) [13](#source-13)

## Sources

1. <a id="source-1"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L116) — `newAgentboxAttachCmd`, help and mutual exclusion.
2. <a id="source-2"></a> [box_use.go](../../cmd/yoloctl/box_use.go#L61) — `boxUseInput`, `isNullDevice`, `callControllerBoxUse`, private channel and receipt cleanup.
3. <a id="source-3"></a> [frontend.go](../../internal/boxuse/frontend.go#L18) — request/response types, `parseRequest`, `Run` input acceptance, history.
4. <a id="source-4"></a> [state.go](../../internal/boxuse/state.go#L19) — bounds, `apply`, `observe`, `wait`, `awaitInput`, `encode`.
5. <a id="source-5"></a> [view.go](../../internal/session/view.go#L38) — `InputActivity` framing and decoder.
6. <a id="source-6"></a> [holder_view_test.go](../../internal/session/holder_view_test.go#L109) — peer activity and per-source bytes.
7. <a id="source-7"></a> [box_use_test.go](../../cmd/yoloctl/box_use_test.go#L104) — routing, input, detach, exit, uncertain setup and holder acceptance.
8. <a id="source-8"></a> [box-use.py](../../scripts/smoke/box-use.py#L87) — disposable-space `run`.
9. <a id="source-9"></a> [controller_client.go](../../cmd/yoloctl/controller_client.go#L310) — frontend routing.
10. <a id="source-10"></a> [holder.go](../../internal/session/holder.go#L533) — `clientReadLoop` admission, per-attachment bytes and activity.
11. <a id="source-11"></a> [protocol.go](../../internal/session/protocol.go#L40) — protocol version 6.
12. <a id="source-12"></a> [state_test.go](../../internal/boxuse/state_test.go#L45) — observations, redraws, holds, waits, acceptance, input and bounds.
13. <a id="source-13"></a> [test_box_use.py](../../scripts/test_box_use.py#L18) — local driver framing and failure checks.
14. <a id="source-14"></a> [box_use_cli.go](../../cmd/yoloctl/box_use_cli.go#L21) — CLI, validation, delegation and completion.
15. <a id="source-15"></a> [batch.go](../../internal/boxuse/batch.go#L31) — `ReadBatch`, `Batch.Run`, `exchange`.
16. <a id="source-16"></a> [batch_test.go](../../internal/boxuse/batch_test.go#L16) — bounds, failures, cleanup and local revisions.
17. <a id="source-17"></a> [tui.go](../../internal/tui/tui.go#L571) — `boxUseReadsStdin`.
18. <a id="source-18"></a> [input.go](../../internal/boxuse/input.go#L21) — `validateInput`.
19. <a id="source-19"></a> [box_use_cli_test.go](../../cmd/yoloctl/box_use_cli_test.go#L102) — finite batch frontend, regular-file/pipe and null-device stdin.
