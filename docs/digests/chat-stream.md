# Managed chat human stream

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] `chat.stream` pins one confined SSH peer, verifies exact container,
declaration (excluding its runtime chat projection), binds and applied
incarnation around probing, then runs a fixed launcher: it copies the published
human client config, setting the nickname and, only in take-over mode,
`regain = true`, into an unlinked, owner-only temporary descriptor and execs
`airc --jsonl 2` on it. Without filesystem `O_TMPFILE` support, exclusive
creation and unlink precede writing any config.
Each stream registers the saved nickname, and one human session holds it. If
the verified server status counts a live human-account connection, a stream
without the `take-over` flag writes only `{"event":"nick-held"}` and fails
before the effect gate, so the receipt never becomes uncertain. The count is an
observation, not a reservation: a session that just closed can still show the
prompt, and Take over then registers with nothing to close. The host asks
the user on `ErrNickHeld`, or on a raced client `NICK_HELD` failure; Esc
closes the view. Take-over uses AIRC's same-account REGAIN. The replaced view
ends with `ErrTakenOver`, from the ERROR record or the closed event's
`server_error`, and never reconnects by itself. Another account's holder fails
as 433, shown as a hard error, and nothing is retried. The stream requires the
client feature `nick-regain`.
[1](#source-1) [2](#source-2) Setup takes no mutation lock, acknowledges the
durable effect gate and releases admission; cancellation closes stalled SSH.
The OpenShell root supervisor owns the pinned Unix socket. [3](#source-3) [4](#source-4)
Closing the view closes only its client. The controller canonicalizes an
allowlist of room send/read/history/members/who/typing/quit and single-channel
join/part/topic operations and connection-wide away/back: no argv, artifact paths or raw IRC. The channel path
requires `client-topic`; see [catalog and stream](chat-channels.md) for current
ACL checks, reload prerequisites and typed multi-channel driving. Host frames are bounded; reads use `next`, not
`latest`. A queued send is pending; complete correlated receipts confirm it.
Epoch changes, gaps, partial frames and disconnects remain explicit, with no
reconnect/resend. Clipboard/link callbacks require a user action and existing
host policy. [5](#source-5) [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] Stream failures distinguish EOF, malformed frames, local deadlines and
transport errors. A failed worker's last 2 KiB of stderr accompanies its exit
error as sanitized single-line text; receipt and retry behavior is unchanged.
The input filter reads a pollable duplicate of inherited worker stdin, so an
early client exit can close and join it while the host keeps stdin open,
preserving the client error instead of waiting for the handshake deadline.
A filter refusal is published before the client sees its EOF, so a client
exiting cleanly on that EOF still reports the refusal.
[9](#source-9) [10](#source-10) [11](#source-11)

[FACT] Only the adapter's own encoding or allowlist, before it invokes the
stream writer, creates a typed local refusal. That send is `rejected`, its
pending slot clears and the same stream continues. After Write, established
definite peer refusals, a `not sent:` reply or a correlated rejected receipt,
remain `rejected` on a live stream. Transport, protocol and unproven peer
failures remain `uncertain` and end the stream: zero or partial bytes, lost,
malformed or uncorrelated replies, and peer errors that only repeat the local
check's text. Nothing is retried. Synthetic-peer regressions inject the former
attachment-capability mismatch into the real channel loop. [12](#source-12) [13](#source-13)

[FACT] Human away/back uses the existing serialized control slot with a 20-second
confirmation deadline. Strict decoding permits only ID/op and the away reason
(1–200 UTF-8 bytes without CR/LF/NUL); it does not acquire a channel grant.
Queue acceptance is not confirmation: only the current server/recipient's 306
or 305 after the pre-submission cursor fence settles its matching action.
Uncertain submission/event loss ends the link without replay. Confirmed own
state patches the exact account plus live nick in roster refreshes/new channels,
never every connection of the account. [14](#source-14)

## Sources

1. <a id="source-1"></a> [Nickname take-over and launcher](../../internal/chatstream/connection.go#L11-L47) — `ErrNickHeld`, `ErrTakenOver`, `NickHeldEvent`, `TakeOverFlag`, `ClientArgv`; embedded client_config.py (`derive`, `main`, `TemporaryFile` unlinks before writes); `runControllerChatStream` refusal at chat_stream.go L53–L65 and `chatStreamTakeOver` L91–L100; `managedChatConnection.Connected` in chat_connection.go L166–L174; driver mapping in channels_client.go `run` L78, channel_events.go `record` L172 and client.go `exchange` L331–L370; host `chatSession.start`/`takeOver` in chat_view.go L266–L396 and chatui `nickHeld`/`overlayTakeOver`.
2. <a id="source-2"></a> [Take-over regressions](../../internal/chatstream/airc_views_integration_test.go#L14) — opt-in real-AIRC `TestRealAIRCHumanViewTakeOver` (NICK_HELD leaves the holder, take-over ends it as `ErrTakenOver` with peer QUIT, another account's 433); takeover_test.go (nick-held frame, ERROR and closed-event mapping); launcher tests in connection_test.go, including `TestClientLauncherEnablesRegainOnlyForTakeOver` and `TestClientLauncherWithoutOTmpfile`; `TestChatStreamUsesTheSavedNickAndRefusesAHeldOneBeforeEffects` in cmd/yoloctl/chat_stream_test.go; `TestNickHeldAsksToTakeOverOrClose` in internal/chatui/takeover_test.go.
3. <a id="source-3"></a> [Pinned connection](../../cmd/yoloctl/chat_connection.go#L44) — peer verification and cancellation.
4. <a id="source-4"></a> [OpenShell v0.0.72](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-process/src/ssh.rs#L53) — `ssh_server_init`.
5. <a id="source-5"></a> [Controller stream](../../cmd/yoloctl/chat_stream.go#L18) — setup and relay.
6. <a id="source-6"></a> [JSONL adapter](../../internal/chatstream/client.go#L158) — `Run`, receipt/echo correlation.
7. <a id="source-7"></a> [Stream allowlist](../../internal/chatstream/protocol.go#L101) — `DecodeRequest`, `Filter`.
8. <a id="source-8"></a> [Host view](../../cmd/yoloctl/chat_view.go#L30) — `runChatView`.
9. <a id="source-9"></a> [Stream diagnostics](../../cmd/yoloctl/chat_stream_stderr.go#L20) — `callChatStream`, bounded `stderrTail`; real controller transport regression in `chat_stream_stderr_test.go`.
10. <a id="source-10"></a> [Frame failures](../../internal/chatstream/client.go#L289-L319) — `closeFor`, `frameError`; deadline and malformed-input regressions in `stream_diag_test.go`.
11. <a id="source-11"></a> [Relay input cleanup](../../cmd/yoloctl/chat_stream.go#L89) — `relayManagedChatWithExec`, `chatFilterInput`; `TestChatRelayExitDoesNotWaitForStdin` in cmd/yoloctl/chat_stream_test.go keeps inherited blocking stdin open with empty or partial input while the client succeeds or fails; `TestChatRelayPreservesFilterFailure` retains a filter refusal; `TestChatRelayReportsRefusalBeforeClientEOF` covers a client that exits cleanly on the refusal's EOF.
12. <a id="source-12"></a> [Pre-write refusal](../../internal/chatstream/client.go#L322-L413) — `localRejection`, `exchange`, `driver.send`.
13. <a id="source-13"></a> [Refusal regressions](../../internal/chatstream/local_rejection_test.go#L131-L295) — `TestChannelsPreWriteRejectionKeepsTheStream` runs `channelDriver.run` with a synthetic peer; `TestChannelsWrittenSendFailuresStayUncertain`.
14. <a id="source-14"></a> [Availability controls](../../internal/chatstream/availability.go#L1) — `availabilityAction`, `availabilityNumeric`, `patchSelfMembers`; protocol.go `decodeChannelRequest`/`FilterChannels`; `TestRealAIRCHumanAvailability` in airc_availability_integration_test.go exercises the managed filter with pinned binaries.
