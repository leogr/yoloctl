# Mermaid admission and fidelity corpus

Verification: `b868ea74f8d99085022bed5f53ef34ecfe3ac740` and accompanying changes.

[FACT] `admit.Check` decides whether a fence may be drawn and never rewrites it:
the renderer gets the original source. It accepts `graph|flowchart` with TD,
TB or LR (RL/BT refused) and `sequenceDiagram`; every non-blank, non-`%%` line
must be recognized, otherwise the fence is refused with `line N: reason`. A
header-less fence (only `;`, blanks or comments) is an empty diagram. Besides
syntax, only what the renderer cannot take is refused: invalid UTF-8 and
control characters other than LF (TAB included, which the worker protocol
refuses). [1](#source-1)

[FACT] Flowcharts admit ids of letters, digits and `_`; plain, quoted, `{…}`
and `(…)` nodes; `-->`, `---`, `-.->`, `==>` with `|label|` or
`-- label -->`; chains; `&` groups; `subgraph id`, `id[title]` or plain-word
titles with `end`. Other shapes, styles (`:::`, `classDef`, `style`,
`click` …), edge variants and quoted subgraph titles (drawn with their quotes)
are refused. Sequences admit `participant`/`actor` with `as`, the arrows
`->>`, `-->>`, `->`, `-->` with `+`/`-`, `Note left of|right of|over`
(two ids only with over), `alt/else/opt/loop/par/and/critical/break … end`,
`activate`, `deactivate` and `autonumber`. [1](#source-1)

[FACT] Admission sets no size, count, label-length or nesting limit: diagram cost is
bounded by the worker's 64 MiB and 2 s per diagram and the 120-row drawing cap
(leogr's approved limits). [1](#source-1) [2](#source-2)

[FACT] The fidelity corpus holds 21 admitted fixtures (the earlier experiment's
samples, back-edge repros, narrow and rich sequences, one construct fixture per
type) and 7 refused ones. A test draws each admitted fixture at 40, 60 and 80
columns with the embedded artifact and requires the stored golden (drawing or
status, e.g. too wide with its columns); `-update` rewrites them, after which
they need a human review. Matching goldens show the artifact is unchanged, not
that every admitted combination draws correctly. [3](#source-3)

## Sources

1. <a id="source-1"></a> [Validator](../../internal/mermaid/admit/admit.go#L1) — `Check`, `checkFlowchart`, `statement`, `node`, `edge`, `subgraphTitle`, `checkSequence`, `message`, `note`.
2. <a id="source-2"></a> [Admission tests](../../internal/mermaid/admit/admit_test.go#L1) — accept/reject table, empty headers, no size limits, unchanged source, corpus split.
3. <a id="source-3"></a> [Fidelity corpus](../../internal/mermaid/fidelity_test.go#L1) — `TestFidelityCorpusMatchesReviewedGoldens` over testdata/fixtures and testdata/golden.
