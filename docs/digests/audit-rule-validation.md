# Rule-selection framing and pinned validator qualification

Verification: `580ea0471331b097af890ea5d69502b9d1468f03` and accompanying changes.

[FACT] `SelectionDocument` preserves the editor fragment's bytes for Falco to
interpret. Its lexical framing requires the first content line to start with
`rules:` at column zero and subsequent content lines to be indented or sequence
entries. BOM, indentation tabs, second root keys, directives and document
markers are refused. The structural check requires one explicit mapping with
only `rules` and an explicit sequence value. Root alias/merge indirection does
not satisfy that check. Blank/comment-only text becomes `rules: []`.
Nested selectors, values and order are not translated into a second rules
language. No selection size, line-length or count limit is imposed.
[1](#source-1) [2](#source-2)

[FACT] The packaged-engine regression constructs a built-in-first ordered
`rules_files` list and includes a verbatim selection fragment through exactly
one controller-chosen `config_files` path with override strategy. Each candidate
runs the same packaged Falco/plugins once with `-c CONFIG --dry-run`, without
a companion `-V`, phase timeout, alternate engine or collector. Caller test
cancellation remains ordinary process cancellation. The optional qualification
requires `FALCO_TEST_ARCHIVES`; without it, no native-engine PASS is established.
[3](#source-3) [4](#source-4)

[FACT] Falco 0.45.2 loads the ordered files and then applies name/tag selections
before its dry-run exit. Rule and tag selectors in one operation are both
applied by the native loader. The test exercises dependencies, overrides,
malformed conditions, unknown fields, wildcard/tag/both selectors, scalar and
alias handling, and engine/plugin requirements. The selection trace case
changes diagnostic verbosity only, retaining the fixed plugins and output
sinks. No imported rule definitions are parsed by this helper.
[3](#source-3) [5](#source-5) [6](#source-6)

[FACT] Exit zero does not mean every imported rule loaded: an unknown source
can be skipped with a warning; unused macros/lists and deprecated append
syntax can also produce warnings. Pinned dry-run warnings are textual loader
diagnostics, including Falco's per-load warning count. A macro referenced by a
later file can already have produced an unused warning in an earlier load.
The regression retains these distinctions rather than treating warnings as
errors or silence as runtime health.
[3](#source-3) [5](#source-5)

[FACT] Dry-run still initializes the configured plugin machinery. It skips
event processing; it does not qualify live capture, metadata attribution,
collector apply/persistence or Linux/macOS runtime parity. These boundaries
remain separate from schema/condition acceptance.
[6](#source-6) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Selection envelope](../../internal/audit/selection.go#L1) — `SelectionDocument`, lexical framing and structural check.
2. <a id="source-2"></a> [Framing regressions](../../internal/audit/selection_test.go#L1) — byte preservation, long comment line, empty input and configuration escapes.
3. <a id="source-3"></a> [Packaged validator regression](../../internal/audit/rules_dryrun_test.go#L1) — `TestPinnedRuleSetDryRun`, single invocation per candidate.
4. <a id="source-4"></a> [Fixed envelope](../../internal/audit/config.go#L1) — `Configuration`, plugin and output choices.
5. <a id="source-5"></a> [Falco 0.45.2 loader](https://github.com/falcosecurity/falco/blob/0.45.2/userspace/falco/app/actions/load_rules_files.cpp#L25) — cumulative ordered loading, warnings and selection loop.
6. <a id="source-6"></a> [Falco 0.45.2 processing](https://github.com/falcosecurity/falco/blob/0.45.2/userspace/falco/app/actions/process_events.cpp#L547) — dry-run before event processing.
7. <a id="source-7"></a> [Falco 0.45.2 initialization](https://github.com/falcosecurity/falco/blob/0.45.2/userspace/falco/app/actions/init_inspectors.cpp#L88) — inspector/plugin initialization.
