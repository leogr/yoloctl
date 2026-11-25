# Sandbox policy DNS and mapping refresh

Verification: `10a95c539a5085045420c7d56d25eeca7b53c4cf` and accompanying changes; v0.1.10 at `f357061bf0f425f6d0a007d6fb31f575f038c827` and accompanying changes, upstream `152c59d3178039e3fbb4a5595f9fff7216a6be92` (v0.1.10, which contains #5650); re-read cited sources when changing behavior.

[FACT] A box resolves a policy-granted host name to a synthetic address that
the network supervisor maps back to the name and its authorized endpoints. A
mapping lives for the DNS answer's TTL, clamped to 1–30 seconds. A synthetic
address is never handed to another name, even after its mapping expires.
[1](#source-1) [2](#source-2)

[FACT] Before #5650 (OpenShell 0.1.6 without yoloctl's patch 9), a TCP connect to an expired synthetic address was denied
with `transparent_tcp_mapping_denied` and returned `EACCES` to the process. A
client that keeps its own DNS answers longer than the mapping hit this on every
later connection. npm 10.9.9 caches lookups for 5 minutes, so `npm install -g`
in a box with the `npm` feature failed on package downloads after the first
requests. [3](#source-3) [4](#source-4)

[FACT] Upstream #5650, part of v0.1.10 (and backported to 0.1.6 as yoloctl patch 9 for boxes created by v0.4.x). A connect to an expired, policy-backed
mapping refreshes it through the same trusted resolver and destination
validation as a box DNS query, then looks it up again
(`PolicyDnsService::lookup_for_connect`). Concurrent connects to one address
share one refresh. A failed refresh backs off for `MIN_MAPPING_TTL` (1 s) and
never authorizes expired upstream addresses. Observation-only mappings, other
ports and mappings from an older policy generation are not refreshed. The
transparent proxy uses this lookup when it stages a connection and again when
it dispatches it, rebuilding the upstream connector from the current pins.
[5](#source-5) [6](#source-6)

[FACT] New boxes run v0.1.10, where the fix is upstream's own; patch 9 is
dropped from the stack. A box created with the nine-patch 0.1.6 stack keeps
its backport until recreated; its recorded release stays readable.
[7](#source-7) [8](#source-8)

[FACT] The supervisor's denial event carries its reason, for example
`NET:OPEN DENIED /usr/bin/node(0) -> 198.18.0.2:443 [reason:transparent_tcp_mapping_denied]`
as shown by `yoloctl box logs` on 0.1.6; v0.1.10 names the real process ID instead of 0 (#5251). [3](#source-3)

## Sources

1. <a id="source-1"></a> [Upstream policy_dns/mod.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-supervisor-network/src/policy_dns/mod.rs#L57-L58) — `MIN_MAPPING_TTL`, `MAX_MAPPING_TTL`; `clamp_mapping_ttl`.
2. <a id="source-2"></a> [Upstream policy_dns/store.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-supervisor-network/src/policy_dns/store.rs#L20-L22) — allocations never reused; `live_record` returns `Expired` (L583-L602).
3. <a id="source-3"></a> [Upstream proxy.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-supervisor-network/src/proxy.rs#L809) — `transparent_tcp_mapping_denied`; `network_broker.rs` `tcp_denial_errno` (L1316) maps `InvalidDestination` to `EACCES`.
4. <a id="source-4"></a> [@npmcli/agent v3.0.0 lib/dns.js](https://github.com/npm/agent/blob/v3.0.0/lib/dns.js#L8-L15) — LRU of 50 lookups, `ttl = 5 * 60 * 1000`; the version bundled with npm 10.9.9 in the box image.
5. <a id="source-5"></a> [Upstream policy_dns/mod.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-supervisor-network/src/policy_dns/mod.rs#L107-L160) — `ConnectLookupError`, `lookup_for_connect` with shared refresh lock and backoff; tests `expired_mapping_refresh_validates_backs_off_and_recovers` (L888), `connect_lookup_rejects_ineligible_refresh_without_dns` (L991).
6. <a id="source-6"></a> [Upstream proxy.rs](https://github.com/ai-openshell/openshell/blob/152c59d3178039e3fbb4a5595f9fff7216a6be92/crates/openshell-supervisor-network/src/proxy.rs#L1210) — `resolve_transparent_target` and dispatch reauthorization.
7. <a id="source-7"></a> [third_party/openshell/pins.json](../../third_party/openshell/pins.json#L1) — the v0.1.10 pin and its eight patches (no patch 9).
8. <a id="source-8"></a> [internal/openshell/release.go](../../internal/openshell/release.go#L1) — `qualifiedPatch9Name` only in `recordedReleases` (0.1.6); `release_test.go` `TestQualifiedPatchStackAcceptsOptionalSeventhAndEighthPatches`, `TestRecordedOpenShell016ManifestsStayReadableForExistingBoxes`.
