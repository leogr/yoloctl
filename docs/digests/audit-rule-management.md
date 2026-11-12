# Native rule generations and host management

Verification: `2cb8f6e9efb3fccaea3f4c48cd2812dbac21f21d` and accompanying changes.

[FACT] Host-only `audit rules list`, `import FILE`, `remove NAME --sha256 PREFIX`
and `edit` use the running matching controller. List creates nothing and never
installs tools. Mutations reuse the ordinary pinned host cache/verified native
installer, so disabled-first preparation does not require prior enablement.
Rule bytes/selection stream separately from the typed request envelope; their
combined size is not the controller frame size. One rule file is at most 2 MiB;
there is no feature count, total-size, selection-size or validation-time limit.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Imported basenames and UTC dates are display metadata. Files are stored
under digest-derived names in private native generations. Built-ins load first;
imports follow in recorded order. Duplicate bytes are a no-op. Removal by an
ambiguous name needs a unique SHA256 prefix of at least eight hex digits.
Built-ins cannot be removed. No imported definitions are parsed by yoloctl.
The fingerprint binds tool/plugin pins, ordered hashes/metadata and verbatim
selection. Reads verify manifests, file/selection hashes and private no-follow
paths; a missing selected generation is an error, never a fallback.
[1](#source-1) [4](#source-4)

[FACT] Preparation writes an unselected generation and invokes the same Falco
once with `-c CONFIG --dry-run`; there is no companion collector, `-V` pass or
validation timer. Falco's own per-load warning headers and full diagnostics are
returned. Exit zero may still skip unknown-source rules; it is not a claim that
every rule is active. Terminal controls are escaped. Rejected/cancelled
preparation leaves the previous native selection and service unchanged.
[1](#source-1) [5](#source-5) [6](#source-6)

[FACT] The editor gets only a `rules:` fragment with commented examples. Human
waiting holds no native mutation lock. Save carries its original revision,
including enablement and build dependencies; stale save refuses and keeps the
host text for reconciliation. Unchanged text is a no-op. Blank/comment-only
input resets to empty selection. Native selectors may disable built-ins or
activate shipped-disabled candidates; list/status expose selection count,
validation warnings and compatibility, separately from runtime health.
[2](#source-2) [3](#source-3) [6](#source-6)

[FACT] Native settings choose the generation under the existing audit lock,
effect gate and receipt scope. Disabled changes persist without starting capture.
Enabled accepted changes restart the collector; the capture gap is intentional.
Health must match epoch, version, built-in digest, generation and effective
fingerprint. Old built-in-only health cannot satisfy new-generation readiness.
A failure after publication is reported, never automatically rolled back or
replayed; unacknowledged service effects retain the existing uncertainty path.
[2](#source-2) [7](#source-7) [8](#source-8)

[FACT] Explicit re-enable revalidates retained imports/selection with current
pins before stopping an old collector. Incompatible data remains inspectable
and retained; it is not replaced with built-in-only rules. A managed generation
already validated before publication is not validated again with `-V` by the
collector. Existing real modern-eBPF readiness remains separate; the legacy
built-in-only service retains its original preflight.
[1](#source-1) [2](#source-2) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Immutable rule store](../../internal/audit/rule_store.go#L1) — `RuleStore.Read`, `Prepare`, `Configuration`, `RulesPinsDigest`.
2. <a id="source-2"></a> [Native transaction](../../cmd/yoloctl/controller_audit_rules_linux.go#L1) — host dispatch, revision check, tool reuse, effect gate and selected apply.
3. <a id="source-3"></a> [Host commands](../../cmd/yoloctl/audit_rules.go#L1) — list/import/remove, streamed data, editor preservation and diagnostics.
4. <a id="source-4"></a> [Store regressions](../../internal/audit/rule_store_test.go#L1) — order, duplicates, rejection, size boundary, selection, tampering and explicit upgrade.
5. <a id="source-5"></a> [One native validator](../../internal/audit/rule_validate_linux.go#L1) — `ValidateRuleSet`, ordinary cancellation, native warning headers.
6. <a id="source-6"></a> [Selection qualification](audit-rule-validation.md) — literal include, framing and warning/skip semantics.
7. <a id="source-7"></a> [Collector](../../internal/audit/collector_linux.go#L1) — selected configuration verification and generation-bound health.
8. <a id="source-8"></a> [Transaction regressions](../../cmd/yoloctl/controller_audit_rules_linux_test.go#L1) — disabled/enabled, rejection, conflict, gate, uncertain stop/start and stale readiness.
