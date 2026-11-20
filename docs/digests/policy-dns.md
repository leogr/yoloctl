# Sandbox policy DNS and mapping refresh

Verification: `10a95c539a5085045420c7d56d25eeca7b53c4cf` and accompanying changes; upstream `ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47` plus `third_party/openshell/patches/` through patch 9, a backport of upstream `4b433e6e3284574866ed0a837a180dbc00ecc800` (#5650); re-read cited sources when changing behavior.

[FACT] A box resolves a policy-granted host name to a synthetic address that
the network supervisor maps back to the name and its authorized endpoints. A
mapping lives for the DNS answer's TTL, clamped to 1–30 seconds. A synthetic
address is never handed to another name, even after its mapping expires.
[1](#source-1) [2](#source-2)

[FACT] Before patch 9, a TCP connect to an expired synthetic address was denied
with `transparent_tcp_mapping_denied` and returned `EACCES` to the process. A
client that keeps its own DNS answers longer than the mapping hit this on every
later connection. npm 10.9.9 caches lookups for 5 minutes, so `npm install -g`
in a box with the `npm` feature failed on package downloads after the first
requests. [3](#source-3) [4](#source-4)

[FACT] Patch 9 backports upstream #5650. A connect to an expired, policy-backed
mapping refreshes it through the same trusted resolver and destination
validation as a box DNS query, then looks it up again
(`PolicyDnsService::lookup_for_connect`). Concurrent connects to one address
share one refresh. A failed refresh backs off for `MIN_MAPPING_TTL` (1 s) and
never authorizes expired upstream addresses. Observation-only mappings, other
ports and mappings from an older policy generation are not refreshed. The
transparent proxy uses this lookup when it stages a connection and again when
it dispatches it, rebuilding the upstream connector from the current pins.
[5](#source-5) [6](#source-6)

[FACT] The backport keeps the fix's production code and its new tests. It
omits the upstream test hunks that change tests absent at this pin
(`proxy/tests/ipv4_mapped.rs` and two tests in `store.rs` and `proxy.rs`), and
adds `connector_for` in place of the newer `pinned_addresses_for` in one
adapted test. `qualifiedPatchStack` accepts the nine-patch chain.
[7](#source-7) [8](#source-8)

[FACT] The supervisor's denial event carries its reason, for example
`NET:OPEN DENIED /usr/bin/node(0) -> 198.18.0.2:443 [reason:transparent_tcp_mapping_denied]`
as shown by `yoloctl box logs`. [3](#source-3)

## Sources

1. <a id="source-1"></a> [Upstream policy_dns/mod.rs at the pin](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-supervisor-network/src/policy_dns/mod.rs#L47-L48) — `MIN_MAPPING_TTL`, `MAX_MAPPING_TTL`; `clamp_mapping_ttl`.
2. <a id="source-2"></a> [Upstream policy_dns/store.rs at the pin](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-supervisor-network/src/policy_dns/store.rs#L20-L22) — allocations never reused; `live_record` returns `Expired` (L544-L563).
3. <a id="source-3"></a> [Upstream proxy.rs at the pin](https://github.com/NVIDIA/OpenShell/blob/ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47/crates/openshell-supervisor-network/src/proxy.rs#L677-L693) — `transparent_tcp_mapping_denied`; `network_broker.rs` `tcp_denial_errno` (L1210-L1218) maps `InvalidDestination` to `EACCES`.
4. <a id="source-4"></a> [@npmcli/agent v3.0.0 lib/dns.js](https://github.com/npm/agent/blob/v3.0.0/lib/dns.js#L8-L15) — LRU of 50 lookups, `ttl = 5 * 60 * 1000`; the version bundled with npm 10.9.9 in the box image.
5. <a id="source-5"></a> [Patch 9](../../third_party/openshell/patches/0009-dns-mapping-refresh.patch#L62-L140) — `ConnectLookupError`, `lookup_for_connect` with shared refresh lock and backoff; README section L10-L22.
6. <a id="source-6"></a> [Patch 9 proxy](../../third_party/openshell/patches/0009-dns-mapping-refresh.patch#L851-L960) — `resolve_transparent_target` and dispatch reauthorization; tests `expired_mapping_refresh_validates_backs_off_and_recovers` (L243), `connect_lookup_rejects_ineligible_refresh_without_dns` (L351), `concurrent_expired_connections_share_one_refresh` (L389), `policy_revocation_during_expired_mapping_refresh_fails_closed` (L429), `dispatch_rebuilds_staged_connector_after_mapping_expiry` (L1104); e2e cached-address probe (L1378-L1387).
7. <a id="source-7"></a> [third_party/openshell/pins.json](../../third_party/openshell/pins.json#L55-L59) — patch 9 entry, sha256 and result tree.
8. <a id="source-8"></a> [internal/openshell/release.go](../../internal/openshell/release.go#L29) — `qualifiedPatch9Name`, `qualifiedPatchStack`; `release_test.go` `TestQualifiedPatchStackAcceptsOptionalSeventhToNinthPatches`.
