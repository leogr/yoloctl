# Audit metadata extraction

Verification: `d79ce925e4f91ea96fecfd03fb1f3e59a3577d22` and accompanying changes.

[FACT] The yolo C++ extraction plugin uses Falco's pinned C ABI 3.12.0
(`falcosecurity/libs` 0.26.1, matching pinned Falco 0.45.2). Its separate required
event schema is explicitly 4.0.0: extraction uses the common event TID and
state tables, not minor-version-specific event parameters. Omitting this export
would make the framework assume schema 3.0.0. The container plugin supplies
`threads.container_id`; extraction reads/releases the triggering thread's table
entry. The plugin exposes only `yolo.box`, `yolo.space`, `yolo.account`,
`yolo.agent`, `yolo.runtime_mode`, `yolo.network_unconfined` and
`yolo.metadata_available`. These are recorded launch context, not per-process
confinement or authority. No per-event RPC, secret lookup or mutation occurs.
[1](#source-1) [2](#source-2) [16](#source-16)

[FACT] After the audit startup attempt, the controller refreshes an observation
on a five-second ticker under native locking and receipt-hold checks.
The bounded runtime projection requests only full container IDs, selected
identity/audit labels and the optional reported privilege flag, without Env,
argv or aggregate labels. Complete schema-1 labels are required; legacy,
invalid, foreign-space or positively mismatched privilege observations are
omitted. Missing runtime privilege evidence does not turn the recorded label
into confinement proof. Failed/held observations do not refresh an old file. One audit maintenance
owner checks expiry before metadata refresh, so metadata failures do not skip
retention; due expiry is retried after refused or failed admission.
[3](#source-3) [4](#source-4) [15](#source-15)

[FACT] Publication is atomic and private on the volatile controller run root.
The stable filename lets compatible running collectors follow controller updates;
the header identifies the publishing controller generation by SHA256. The
version-1 tab-separated protocol has a timestamp/count/generation header and
up to 4096 full-ID records, capped at 2 MiB. Each row has only validated
box/space/account/agent names, recorded runtime mode and network boolean.
Go's encoder and C++ parser share a checked fixture. Generation changes and
container/name reuse never join records by name. [5](#source-5) [6](#source-6) [12](#source-12)

[FACT] The plugin refreshes its local file cache at most once per second.
It requires a regular, root-owned, mode-0600, single-link file, no-follow open,
complete bounded contents and a valid generation identifier. Snapshots older than
15 seconds or more than two seconds in the future are unavailable. Full IDs
match exactly; 12-character IDs require one unambiguous full-ID prefix. Missing,
stale, invalid, host, absent-thread or ambiguous identity produces unavailable
metadata. A present false availability field prevents fallback to older label
values; the event itself remains visible. [1](#source-1) [6](#source-6)
[7](#source-7)

[FACT] Ordinary Go builds embed qualified Linux amd64/arm64 shared objects,
source/recipe/header/artifact pins and runtime notices. Maintenance uses exact
Zig 0.15.2 with glibc-2.28 targets and a static C++ runtime; the checker rebuilds
both targets twice with separate caches and compares tracked bytes. Go tests
check ELF architecture/exports, hashes, source/recipe consistency and notices.
Immutable tool directories include the plugin digest; the audit rules pin also
binds its qualification manifest, so updates need explicit enablement.
[8](#source-8) [9](#source-9) [10](#source-10) [13](#source-13)

[FACT] C++ fixtures use synthetic events/tables and load the native DSO to check
API/schema/version exports. The opt-in Go archive test runs native Falco `-V`
and opens a no-driver engine with `-M 1`. This mode alone does not prove schema
admission or BPF capture. `FALCO_TEST_ENGINE=modern_ebpf` instead requires a
successful real modern-engine open; schema/permission/load failures fail that
qualification. [11](#source-11) [14](#source-14)

## Sources

1. <a id="source-1"></a> [Plugin adapter](../../third_party/audit-plugin/plugin.cpp#L1) — initialization, thread-table extraction, read-only refresh and field ownership.
2. <a id="source-2"></a> [ABI pins](../../third_party/audit-plugin/include/pins.json#L1) — exact headers, version and license provenance.
3. <a id="source-3"></a> [Publisher](../../cmd/yoloctl/controller_audit_metadata_linux.go#L1) — native guard, holds, filtering and periodic publication.
4. <a id="source-4"></a> [Runtime projection](../../internal/container/audit_metadata.go#L1) — `AuditLabels`, bounded transport and complete ID matching.
5. <a id="source-5"></a> [Snapshot encoder](../../internal/auditmeta/snapshot.go#L1) — format and record bounds.
6. <a id="source-6"></a> [C++ parser](../../third_party/audit-plugin/snapshot.hpp#L1) — strict rows, freshness and unique identity lookup.
7. <a id="source-7"></a> [Privacy parser](../../internal/audit/types.go#L1) — availability precedence and selected fields.
8. <a id="source-8"></a> [Regeneration](../../scripts/build-audit-plugin.py#L1) — exact toolchain and independent-cache reproduction.
9. <a id="source-9"></a> [Immutable installation](../../internal/audit/artifacts.go#L1) — `InstalledDir`, `Install`, `VerifyInstalled`.
10. <a id="source-10"></a> [Config pin](../../internal/audit/config.go#L1) — plugin order, snapshot path and dependency-bound `RulesDigest`.
11. <a id="source-11"></a> [C++ fixtures](../../third_party/audit-plugin/test_plugin.cpp#L1) — parser and synthetic event/table cases.

12. <a id="source-12"></a> [Private paths](../../internal/layout/audit.go#L1) — stable path and publishing generation identifier.
13. <a id="source-13"></a> [Artifact checks](../../internal/auditplugin/plugin_test.go#L1) — ELF/hash/source/recipe and notices validation.
14. <a id="source-14"></a> [Native validation](../../internal/audit/audit_test.go#L1) — `TestPinnedArchivesInstallVerifyAndRuleSyntax`.

15. <a id="source-15"></a> [Audit maintenance](../../cmd/yoloctl/controller_audit_linux.go#L1) — `startAuditRetention`, startup ordering and due-expiry retry.

16. <a id="source-16"></a> [Event schema contract](../../third_party/audit-plugin/include/plugin/plugin_api.h#L1148) — required minimum schema callback; absent/null defaults to 3.0.0, separate from API version.
