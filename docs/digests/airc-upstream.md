# Reviewed AIRC client and server contracts

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; upstream source verified at AIRC `b76ba355863cc3eb51900a313d7647d9190eedfa`.

[FACT] The manifest selects the packaged revision/checksum. Reviewed
contracts do not establish installed support; probe actual binaries.
Features include `artifact-cli`, `artifact-metadata`, `named-waiter-status`,
`named-delivery`, `named-delivery-receipts`, `named-show`,
`channel-delete-reload`, `message-attachments` (see
[staged attachments](airc-attachments.md)) and `nick-regain`.
[5](#source-5) [8](#source-8) [18](#source-18)

[FACT] Nick regain applies only to an authenticated, unregistered connection
that negotiated `airc.dev/nick-regain`. A same-account holder makes NICK fail
as `FAIL NICK NICK_HELD`. REGAIN closes that holder with ERROR/QUIT
`Session taken over`, and another account's holder stays 433. The client
sends NICK after SASL, `regain` defaults to false, and the JSONL v2 closed
event names `server_error`. [22](#source-22)

[FACT] Named IPC remains version 2; saved state is normally version 3, becoming
version 4 once a receipt request is saved. State v1/v2 upgrades only on explicit
connect; older binaries refuse newer state. Disconnect workers before
upgrading. Bounds: 512 events/4 MiB rendered, 1 MiB reads, 16 MiB state, 2 MiB
requests, 32 send/32 control receipts. State defaults to XDG state or
`~/.local/state/airc`. Waits never change read cursors.
[1](#source-1) [20](#source-20)

[FACT] Named controls use random 32-hex operation IDs; identical retained
requests return saved outcomes, conflicting reuse refuses. Intent/receipt saves
precede one write. Controls share sends' pending slot. Unconfirmed controls
expire after ten seconds, become uncertain and quarantine join/part targets.
Startup 403/474 preserves other channels; KICK quarantines without automatic rejoin.
[2](#source-2) [3](#source-3)

[FACT] Named clients/JSONL v2 negotiate multiline and correlate complete echoes;
v2 exposes pending/later receipts and topic controls. v1 retains queued
sends/fallback lines. Explicit recovery never resends: joined channels get
at most six pages of twenty within twenty seconds overall; fresh JOIN queues
LATEST 20 (128 queued, twenty seconds each). Away/back requires 306/305, resets on disconnect and grants no authority. USERHOST returns up to five
registered nicks; it and WHOIS retain server-wide discovery.
[2](#source-2) [3](#source-3) [4](#source-4)

[FACT] Status/reload prove generation, exact configuration SHA-256 and process
incarnation. Explicit `removed_channels` purges messages, published/staged
artifacts, bans and settings. Removals, additions and fallible preparation share
one transaction before publication/KICK; failure rolls back. Startup reapplies
intent. Unlisted removal/rename refuses. Upload admission captures a generation;
the actor rejects uploads whose channel was subsequently replaced. Unrelated
channels remain live. ACL KICK does not revoke accounts. Lost replies require
inspection. [4](#source-4) [18](#source-18)

[FACT] Finite authenticated HTTP uploads require a joined speaking session and
matching size/hash receipts. Post-send errors never authorize retry. Downloads
reject redirects, enforce origin/size and exclusively create private destinations;
header filenames never select paths. Defaults remain 4 MiB/32 artifacts per channel.
[9](#source-9) [10](#source-10)

[FACT] Server-owned artifact UUID/name/size/hash metadata persists atomically
with messages/history. Named records validate it; inbound lookalikes cannot
establish trusted metadata, image type or URL-fetch authority.
[10](#source-10) [11](#source-11)

[FACT] Plain `wait` defaults to messages; mentions/all are explicit. It consumes
nothing and debounces two seconds quiet/ten seconds per burst; critical/gap/
pressure bypass delay. Unlimited callers renew bounded requests; errors end
them. `status` reports the current worker registration, not future model wake.
Legacy bookkeeping uses private no-follow, locked/synced files and launches nothing.
[1](#source-1) [12](#source-12)

[FACT] `subscribe` and `wait --deliver` use a separate durable cursor and one
connection-bound consumer. UUID batches persist before handoff; restart requires
explicit ID-bound recovery. Defaults are 8 KiB with truncation/lookup hints.
`show`/`--parent`/`--chain` fetch bounded context without moving cursors.
`airc-codex` uses literal `codex queue` argv, capped at 64 KiB/thirty seconds.
A clean exit and matching thread receipt prove queue acceptance, not model
processing; uncertainty stops delivery without retry. [14](#source-14) [15](#source-15)

[FACT] Opt-in `airc-codex --wait-for-idle` queues once after a fresh read-only
`idle` status from `$CODEX_HOME/app-server-control`; `--idle-timeout` (0 = none)
bounds the wait, socket retries included. Refusal, expiry or a signal exits 3
unsent; a signal during queueing exits 4. `subscribe --sink-timeout 0` (exec
adapters; default 60) drops only the adapter deadline. The recipe keeps the
default route first. [23](#source-23) [24](#source-24)

[FACT] Separate opt-in `--app-server --app-server-socket PATH` uses only an
existing private same-user daemon and exact loaded thread: `turn/start` when
idle, `turn/steer` with the discovered active ID when busy. One exact no-active
rejection allows one reclassification. A locked/synced per-batch journal precedes
submission and receipt acceptance; retained accepted/uncertain submissions are
never resent automatically. Default queue and idle-wait routes stay unchanged.
No interrupt, execution-setting override or approval response is sent; approval
routing remains untested. [25](#source-25) [26](#source-26)

[FACT] Scoped receipt holds are opt-in (1–3600 s, at least the minimum interval,
never with wrap). After acceptance, later batches wait for a matching receiver
report, expiry, urgent events or retention pressure. Reporting proves receipt
only; existing provider queues are not drained or capped. Named records,
replies and confirmed sends use shared `m:` references; `show`/`--reply` also
take full msgids and refuse `@N`. [20](#source-20) [21](#source-21)

[FACT] Compact headers retain ID/session: omitted events/context mean 1/0,
true flags are bare words, events=0 explicit. First/GAP/retry/inspection notices
reserve space for saved settings. Status exposes current settings; legacy
headers stay verbatim. [19](#source-19)

[FACT] `subscribe --wrap N` (64–65536) splits lines after the header longer
than N bytes at char boundaries into `| ` continuations, so no split begins a
record- or header-like line. The `airc-delivery` Claude Code plugin's launcher
waits for `connected;`, pauses 2 s once while a consumer remains, runs
`subscribe --max-bytes 2560 --wrap 480 -- -`, forwards HUP/INT/TERM and prints
one stop line when it exits, never restarting. [16](#source-16) [17](#source-17)

[FACT] Tests cover HTTP, forged metadata, replay and waiters. Opt-in real-binary
Unix fixtures cover channel/ACL controls and join/topic/history/send/roster;
peer verification is stubbed. They do not validate images/deployments, VM
confinement, decoded images or native UI.
[5](#source-5) [6](#source-6) [7](#source-7) [13](#source-13)

## Sources

1. <a id="source-1"></a> [AIRC b1e88a8 sessions](https://github.com/leogr/airc/blob/b1e88a82d83554b94ff53a04cb2fa8563bf6e37b/src/agent_session/mod.rs#L32) — IPC, wait/serve; state version/upgrade and storage publication.
2. <a id="source-2"></a> [AIRC controls](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/src/agent_session/channels.rs#L19-L1033) — receipts/quarantine and history recovery.
3. <a id="source-3"></a> [AIRC client](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/src/client.rs#L181-L494) — `connect_named`; JSONL/topic L599–1155.
4. <a id="source-4"></a> [AIRC server](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/src/server/core.rs#L130-L422) — reload/status; presence L1820–1946; `store.rs:37–98` schema/addition.
5. <a id="source-5"></a> [AIRC features](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/src/lib.rs#L14-L41) — `FEATURES`.
6. <a id="source-6"></a> [Real server fixture](../../internal/managedchat/airc_channel_integration_test.go#L109) — `TestRealAIRCChannelAdditionAndACLRemovalWithoutRevocation`.
7. <a id="source-7"></a> [Real client fixture](../../internal/chatstream/airc_channels_integration_test.go#L297) — `TestRealAIRCRunChannelsTopicHistoryAndACLRemoval`.
8. <a id="source-8"></a> [Source pin](../../internal/features/airc-source/manifest.json#L1) — release source commit and per-architecture archive checksums.
9. <a id="source-9"></a> [Finite CLI](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/src/bin/airc.rs#L101-L195) — dispatch; `client.rs:1157–1327` HTTP/bounds; `config.rs:108–125` defaults.
10. <a id="source-10"></a> [Artifact commit](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/src/server/core.rs#L2116-L2187) — access/metadata; `store.rs:150–179` transaction.
11. <a id="source-11"></a> [Named metadata](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/src/agent_session/state.rs#L785-L816) — validation; rendering L352–401, replay test L852–894; `core.rs:1133,1443` strips forged tags.
12. <a id="source-12"></a> [Monitor helper](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/skills/airc/scripts/airc-monitor.py#L28-L157) — `locked/read/save/main`; `scripts/test_airc_monitor.py:1–78` boundaries.
13. <a id="source-13"></a> [Artifact regressions](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/tests/collaboration.rs#L1629-L1912) — forgery/finite HTTP; `tests/managed.rs:86–208` Unix download; `tests/named_sessions.rs:383–440` waiter lifecycle.
14. <a id="source-14"></a> [AIRC delivery](https://github.com/leogr/airc/blob/b1e88a82d83554b94ff53a04cb2fa8563bf6e37b/src/agent_session/state.rs#L591-L796) — cursor/pending state; `delivery.rs:12–254` bounds/header; `mod.rs:326–445,713–968,1217–1342` lookup, sink and consumer/recovery.
15. <a id="source-15"></a> [Codex adapter](https://github.com/leogr/airc/blob/b1e88a82d83554b94ff53a04cb2fa8563bf6e37b/src/bin/airc-codex.rs#L1-L220) — bounded queue adapter and receipt; tests in `tests/codex_adapter.rs`.
16. <a id="source-16"></a> [AIRC wrap](https://github.com/leogr/airc/blob/7cb0c646101c7e64a61f6cfa11cfdc3e06f71c23/src/agent_session/delivery.rs#L174-L204) — `wrap`; `mod.rs:697,733–740,831–834` option and use.
17. <a id="source-17"></a> [Claude plugin launcher](https://github.com/leogr/airc/blob/7cb0c646101c7e64a61f6cfa11cfdc3e06f71c23/integrations/claude-plugin/scripts/airc-delivery#L1-L41) — launcher; `tests/claude_plugin.rs:1–236` regressions.
18. <a id="source-18"></a> [AIRC deletion](https://github.com/leogr/airc/blob/0c2c8bd4f46e21a682ddd02083408a54854b2c35/src/server/core.rs#L257-L402) — reload; `store.rs:64–150,446–455` transaction/purge; `http.rs:129–178`, `core.rs:2367–2387` upload epochs; `lib.rs:30` feature.
19. <a id="source-19"></a> [Compact delivery](https://github.com/leogr/airc/blob/0c2c8bd4f46e21a682ddd02083408a54854b2c35/src/agent_session/delivery.rs#L79-L303) — saved settings/render/header; `state.rs:622–854` assembly; `mod.rs:313–331` consumer status.

20. <a id="source-20"></a> [AIRC receipt implementation](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/src/agent_session/state.rs#L34-L43) — v3/v4; receipt/hold transitions L721–1138, option validation in `mod.rs` L1070–1183 and hold gating L1689–1732.
21. <a id="source-21"></a> [AIRC skill reference contract](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/skills/airc/SKILL.md#L188-L196) — shared references; scoped notice L126–147, Codex recipe L83–116 in `references/codex.md`; codec in `src/agent_session/reference.rs`.
22. <a id="source-22"></a> [AIRC nick regain](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/src/server/core.rs#L840-L890) — `nick`; client `open_connection` in `src/client.rs` L284–374, closed event L988–1002, tests in `tests/collaboration.rs` L605–729.
23. <a id="source-23"></a> [AIRC wait for idle](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/src/bin/airc-codex.rs#L55-L230) — `--wait-for-idle`, `--idle-timeout`, `wait_for_idle`; the status client in `src/bin/airc-codex/idle.rs` (L1-L972).
24. <a id="source-24"></a> [AIRC unbounded sink and recipe](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/src/agent_session/mod.rs#L734-L795) — `sink_timeout` validation; `run_sink` call at L889; recipe section in `skills/airc/references/codex.md` L120-L165.

25. <a id="source-25"></a> [AIRC app-server adapter](https://github.com/leogr/airc/blob/8a6cfa5aff1daa7b9d63ce786b32bce8af17db32/src/bin/airc-codex/app_server.rs#L1) — endpoint/peer binding, active-ID pagination, typed receipts and bounded reclassification.
26. <a id="source-26"></a> [AIRC submission journal](https://github.com/leogr/airc/blob/8a6cfa5aff1daa7b9d63ce786b32bce8af17db32/src/bin/airc-codex/app_server/journal.rs#L1) — strict version-1 records, stable owner lock, descriptor-relative no-follow I/O and durable acceptance/replay protection.
