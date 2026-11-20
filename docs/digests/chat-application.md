# Managed chat membership application

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] Server mounts are config RO, data RW, human leaf RO, endpoint RW and
control RW. Participants receive their own credential leaf and endpoint RO.
Control stays server-only under `/run/yoloctl/chat-control`; VM endpoint parents
use `/run/yoloctl/controller/{chat,chat-control}/<room-id>`. Directory binds
preserve atomic replacement. Verification rejects foreign, duplicate, writable
or overlapping binds. Discovery uses the confined child environment channel;
credentials remain 0600 files. [1](#source-1)
[2](#source-2)

[FACT] Rendering verifies desired state, ownership and collision-free immutable
account encodings. Nicknames use enrollment order, human first, with immutable
collision fallbacks. Private `identity.json` maps authenticated identities;
nicknames confer no authority. Client files publish before server TOML. Revoked
recovery leaves remain; server ACL denial and client disconnection enforce
revocation. [3](#source-3)

[FACT] Controller-derived operator roles, persistent human nicknames and
identity-guarded participant attachment use the existing native lifecycle;
see [roles, settings and attachment](chat-settings.md) for their proof and
explicit reconnect/start boundaries. Role synchronization retains published
changes as pending when the holder is stopped and control is absent/refused,
even inside a running container; verified live orphans still reload once.
Explicit ensure owns startup. [4](#source-4)

[FACT] Applied status requires exact generation/SHA256/account count, Unix IRC
and artifact listeners, no revoked connections, a live holder and verified
container binds. SO_PEERCRED plus the container's cgroup in the guest's
workload pool (systemd slice or microvm group) binds control responses to
runtime. Control allows one Unix request per connection, 16 KiB requests and
1 MiB responses, without reload replay. Failed application retains desired
state as pending; `server_ready` differs from complete `runtime_applied`.
Feature probes require client/server presence support. Reload is followed by
fresh status proof; no TCP fallback exists.
[5](#source-5) [6](#source-6) [7](#source-7) [8](#source-8) [9](#source-9)

[FACT] Tests cover projection/ownership, failed writes, identity reuse, application
proof, frames/receipts and archive independence. Real kernel locks, subprocesses
and Unix/SSH fixtures cover read/open contention, cancellation and replacement;
runtime/cgroup inspection is stubbed. Opt-in real AIRC control tests also stub
peer verification. None proves VM authority/confinement, RO-bind connectivity,
SQLite quiescence or graphical fidelity; isolated host validation remains required.
[10](#source-10) [11](#source-11)
[12](#source-12) [13](#source-13) [14](#source-14)
[15](#source-15) [16](#source-16) [17](#source-17) [18](#source-18)

## Sources

1. <a id="source-1"></a> [Box projection](../../internal/agentbox/chat.go#L15) — creation, discovery, drift and live verification.
2. <a id="source-2"></a> [Native binds](../../internal/managedchat/projection.go#L13) — `Projection`, `EnableRuntime`, `PrepareEndpoints`.
3. <a id="source-3"></a> [Renderer](../../internal/managedchat/render.go#L22) — accounts, nicknames, identity map, snapshot publication.
4. <a id="source-4"></a> [Settings and roles dispatcher](../../cmd/yoloctl/chat_runtime.go#L40) — admitted settings/role mutations and observational attachment lookup.
5. <a id="source-5"></a> [Runtime service](../../cmd/yoloctl/chat_runtime.go#L39) — dispatcher, `ensureManagedRoom`, `ensureManagedServer`, membership apply/recreate.
6. <a id="source-6"></a> [Control proof](../../internal/managedchat/control.go#L17) — `Applied`, `Control.Call`.
7. <a id="source-7"></a> [Observation](../../cmd/yoloctl/chat_status.go#L16) — `observeManagedChat`.
8. <a id="source-8"></a> [Linux peer](../../cmd/yoloctl/chat_peer_linux.go#L17) — `verifyChatControlPeer`, `chatPeerCollector`; `workloadPoolRel` in workload_pool.go.
9. <a id="source-9"></a> [Listener wire contract](../../internal/managedchat/control_test.go#L119) — literal status JSON, Unix-only endpoints and rejected shapes.
10. <a id="source-10"></a> [Availability](../../cmd/yoloctl/chat_availability_test.go#L26) — actual handlers and lock contention.
11. <a id="source-11"></a> [Connection](../../cmd/yoloctl/chat_connection_test.go#L157) — real Unix/SSH fixtures; runtime authority stubbed.
12. <a id="source-12"></a> [Runtime regressions](../../cmd/yoloctl/chat_runtime_test.go#L16) — reserved ownership, partial inventory, name reuse and operator/human operation separation.
13. <a id="source-13"></a> [Stream regressions](../../internal/chatstream/client_test.go#L19) — receipts, spoofing, epochs, gaps and disconnects.
14. <a id="source-14"></a> [Proof regressions](../../internal/managedchat/control_test.go#L17) — exact proof and rejected peer/bounded response tests.
15. <a id="source-15"></a> [Nickname regressions](../../internal/managedchat/nickname_test.go#L8) — case collisions and immutable fallbacks.
16. <a id="source-16"></a> [Request bounds](../../internal/chatstream/protocol_test.go#L10) — foreign authority and partial frame rejection.
17. <a id="source-17"></a> [Archive independence](../../internal/spacearchive/chat_test.go#L65) — fresh identities/credentials and retained synthetic data.
18. <a id="source-18"></a> [Real control interoperability](../../internal/managedchat/airc_integration_test.go#L22) — `TestRealAIRCControlContract`; opt-in published server, stubbed local peer verification.
