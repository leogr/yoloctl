# Native rule generations and host management

Verification: `580ea0471331b097af890ea5d69502b9d1468f03` and accompanying changes.

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
input resets to empty selection. The exact help prefix is added only when absent,
so saved help does not accumulate on reopening. Native selectors may disable built-ins or
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

[FACT] Before receipt completion, successful mutations retire verified,
unreferenced generated directories. The current operation's prior selection,
new selection and service reference survive; the prior can retire on the next
admitted change. Unreadable or unlisted generation entries are retained and
reported together after the pass, without blocking retirement of other verified
unreferenced generations. Unknown service references still prevent the pass.
Failed attempts
before desired-state publication discard their own prepared generation.
[2](#source-2) [9](#source-9)

[FACT] Space backup includes selected rule bytes (including their shipped-file
snapshot), order, original metadata and selection. Import retains that source
with audit disabled; it excludes tools, logs, attempts, health and generated
absolute configurations. Explicit enable prepares and validates a current
configuration. Whole-archive format limits remain those of space backup, not a
new rules count/total/selection capacity. Unknown events use a fixed template
over the existing selected fields; raw custom output/argv/env stay excluded.
Reusing a built-in name still uses its shipped name-keyed template, whose text
may not describe the custom condition. [10](#source-10) [11](#source-11)

## Sources

1. <a id="source-1"></a> [Immutable rule store](../../internal/audit/rule_store.go#L1) — `RuleStore.Read`, `Prepare`, `Configuration`, `RulesPinsDigest`.
2. <a id="source-2"></a> [Native transaction](../../cmd/yoloctl/controller_audit_rules_linux.go#L1) — host dispatch, revision check, tool reuse, effect gate and selected apply.
3. <a id="source-3"></a> [Host commands](../../cmd/yoloctl/audit_rules.go#L1) — list/import/remove, streamed data, editor preservation and diagnostics.
4. <a id="source-4"></a> [Store regressions](../../internal/audit/rule_store_test.go#L1) — order, duplicates, rejection, size boundary, selection, tampering and explicit upgrade.
5. <a id="source-5"></a> [One native validator](../../internal/audit/rule_validate_linux.go#L1) — `ValidateRuleSet`, ordinary cancellation, native warning headers.
6. <a id="source-6"></a> [Selection qualification](audit-rule-validation.md) — literal include, framing and warning/skip semantics.
7. <a id="source-7"></a> [Collector](../../internal/audit/collector_linux.go#L1) — selected configuration verification and generation-bound health.
8. <a id="source-8"></a> [Transaction regressions](../../cmd/yoloctl/controller_audit_rules_linux_test.go#L1) — disabled/enabled, rejection, conflict, gate, uncertain stop/start and stale readiness.

9. <a id="source-9"></a> [Generation retirement](../../internal/audit/rule_retire.go#L1) — protected references, owned inventory and exact-path retirement.
10. <a id="source-10"></a> [Archive adapter](../../internal/spacearchive/audit.go#L1) — selected-source inventory, hashes, disabled restoration; native publication in internal/audit/rule_restore.go.
11. <a id="source-11"></a> [Fixed output adapter](../../internal/audit/output.go#L1) — `outputTemplate`, `RenderOutput`, `FormatPrettyOutput`; custom privacy regressions in custom_output_test.go.
