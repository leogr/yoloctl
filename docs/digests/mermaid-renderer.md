# Pinned Mermaid WASI artifact

Verification: `85f67be08d8bceefb03db745cad123ea5bbade49` and accompanying changes.

[FACT] `internal/mermaid` embeds a Mermaid text-rendering WASI command and its
manifest. Ordinary Go builds use the committed bytes; regeneration is a
maintenance operation requiring Rust 1.95.0 and `wasm32-wasip1`. The pins name
mermaid-text 0.58.0, its registry checksum and upstream revision, the ordered
patch stack, locked dependencies, wrapper and builder hashes, and artifact hash.
The manifest binds these inputs to the embedded bytes. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] The patch stack contains sequence width fitting and non-rectangle
back-edge port spreading, followed by the back-edge regression and its lint
correction. The crate archive's original Cargo manifest is restored during
preparation so added integration tests are discoverable. Upstream and
transitive notices, plus the pinned Rust standard-library notices, are retained
beside the pins. [2](#source-2) [3](#source-3)

[FACT] The fixed WASI command accepts one width argument from 1 through 4096
and nonempty UTF-8 source read to the end of stdin (bounded only by the guest
memory the Go owner grants). It delegates to
`render_with_options` with strict width. Exit 2 rejects input/arguments, 3 is a
renderer error, 4 reports too-wide output with its required column count on
stderr, and 5 reports an output-write error. A successful drawing goes to
stdout. This command does not implement UI admission, process supervision or
render scheduling. Embedding the artifact alone does not enable Mermaid fences
in chat or follow. [4](#source-4) [5](#source-5)

[FACT] The builder verifies source and input hashes, applies patches without
fuzz, uses a locked release/LTO/abort build, and remaps source/cache paths.
Its isolated Cargo graph uses recipe/package/version/target/cfg-derived crate
metadata: source-path remapping alone does not normalize Cargo's crate identity.
`--check` rebuilds in a fresh temporary root and requires exact artifact,
manifest and pinned-output equality. It does not change committed files.
[3](#source-3) [6](#source-6)

[FACT] Tests validate committed inputs and instantiate the real module through
wazero with fresh instances for flowchart, sequence, strict-width and invalid
input controls. These finite artifact tests do not establish drawing fidelity
for all inputs, native macOS execution, a total RSS bound or a UI cancellation
guarantee. [5](#source-5)

## Sources

1. <a id="source-1"></a> [Embedding](../../internal/mermaid/artifact.go#L1).
2. <a id="source-2"></a> [Pins](../../third_party/mermaid-text/pins.json#L1), [manifest](../../internal/mermaid/wasm/manifest.json#L1) and adjacent patches/licenses.
3. <a id="source-3"></a> [Builder](../../scripts/build-mermaid.py#L1) — `load_inputs`, `prepare`, `build_env`, `collect_licenses`, `main`.
4. <a id="source-4"></a> [WASI command](../../third_party/mermaid-text/worker/src/main.rs#L1).
5. <a id="source-5"></a> [Artifact tests](../../internal/mermaid/artifact_test.go#L1) — `TestArtifactManifestMatchesPinnedInputs`, `TestPinnedWASICommand`.
6. <a id="source-6"></a> [Crate identity](../../third_party/mermaid-text/rustc-repro.py#L1) and [maintenance regressions](../../scripts/test_mermaid_build.py#L1).
