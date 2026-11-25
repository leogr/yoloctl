# Third-party notices

yoloctl's original code is licensed under [MIT](LICENSE), copyright 2026
Leonardo Grasso. Imported code, libraries, embedded artifacts and programs
shipped with a VM retain their respective licenses and copyright notices.
The root MIT license does not replace those terms.

## Source notice index

| Component | Version/source input | License | Copyright/attribution | Source | License and NOTICE text |
|---|---|---|---|---|---|
| Ultraviolet renderer | `878653296cfd` plus local patches | MIT | Copyright 2025 Charmbracelet, Inc | [Pinned source and patches](third_party/README.md#pinned-host-renderer) | [Original LICENSE](third_party/ultraviolet/LICENSE) |
| sftp/v2 | `v2.0.0-alpha2` plus local patch | BSD-2-Clause | Copyright 2013 Dave Cheney | [Local source and provenance](third_party/README.md#pinned-sftp-server) | [Original LICENSE](third_party/sftp-v2/LICENSE) |
| Ghostty terminal core | `vt-1.0.2 (573c4a21e02311ae87844af26b6a7a179f82858e)` | MIT | Copyright 2024 Mitchell Hashimoto and Ghostty contributors | [Artifact/source manifest](internal/terminal/wasm/manifest.json) | [Ghostty LICENSE](internal/terminal/wasm/LICENSE-ghostty) |
| Zig code used by the terminal core | `0.17.1` build input | MIT | Copyright Zig contributors | [Manifest](internal/terminal/wasm/manifest.json) | [Zig LICENSE](internal/terminal/wasm/LICENSE-zig) |
| uucode and its data/components | Terminal core's pinned source input | MIT and included component/data notices | Copyright 2026 Jacob Sandlund; other notices in the full text | [Pinned terminal core](third_party/README.md#pinned-terminal-core) | [Full uucode license/notices](internal/terminal/wasm/LICENSE-uucode) |
| Unicode data | Terminal core's pinned source input | Unicode License V3 | Copyright 1991–2025 Unicode, Inc | [Unicode](https://www.unicode.org/license.txt) | [Unicode license](internal/terminal/wasm/LICENSE_unicode) |
| UTF-8 decoder | Terminal core's pinned source input | MIT | Copyright 2008–2009 Bjoern Hoehrmann | [Upstream decoder](https://bjoern.hoehrmann.de/utf-8/decoder/dfa/) | [Original notice](internal/terminal/wasm/LICENSE_Bjoern_Hoehrmann) |
| AIRC | `v0.1.3` release archives (source `9a7953bb5e417974ae3a9300bda3853666d341c8`) | MIT; dependencies retain their terms | Copyright 2026 Leonardo Grasso | [Pinned manifest](internal/features/airc-source/manifest.json), [source license](https://github.com/leogr/airc/blob/9a7953bb5e417974ae3a9300bda3853666d341c8/LICENSE) | [Preserved AIRC LICENSE](LICENSES/AIRC-MIT.txt); Rust inventory below |
| OpenShell runtime (legacy) | `v0.0.72`; no longer provisioned by new VM images after the 0.1.6 upgrade, present only in VM images built before it | Apache-2.0; dependencies retain their terms | Upstream copyright holders; see upstream source and retained headers | [Pinned VM inputs](assets/vm/firecracker/pins.json), [upstream](https://github.com/NVIDIA/OpenShell/tree/v0.0.72) | [Preserved upstream LICENSE](LICENSES/OpenShell-v0.0.72-LICENSE.txt), [Apache text](LICENSES/Apache-2.0.txt) |
| OpenShell runtime | `v0.1.10` built from upstream commit `152c59d3178039e3fbb4a5595f9fff7216a6be92` plus eight yoloctl patches (result trees: patch 1 `46bcd43d1ee03890d995f6605b5986d07818730c`; patch 2 `c19e801818b6008b3c09d7483c006a24d3c0596e`; patch 3 `05f29cdee4d0644258ca29c7a664ffc8c27963e4`; patch 4 `ccc654490d2be55c9076dd3d2aa808a9fee45700`; patch 5 `293c216160c664e4419ff11a0487c7ac641a1907`; patch 6 `d974ffa9c31551ca93ceee99ce9e61df50bbd6e5`; patch 7 `4187e93044db91c260487bc74e256e98f82f1429`; patch 8 `e3704049c286549542c5ff3ecff7c3dc90aa421d`), embedded in yoloctl for Linux amd64 and arm64; boxes created before keep their recorded `v0.1.6` runtime until recreated | Apache-2.0; dependencies retain their terms | Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES; upstream copyright holders and retained headers | [Upstream v0.1.10 source](https://github.com/ai-openshell/openshell/tree/152c59d3178039e3fbb4a5595f9fff7216a6be92), [yoloctl patches](third_party/openshell/patches/0001-unmediated-network.patch) ([patch 2](third_party/openshell/patches/0002-workspace-read-only.patch), [patch 3](third_party/openshell/patches/0003-v0072-syscall-parity.patch), [patch 4](third_party/openshell/patches/0004-v0072-process-group-signals.patch), [patch 5](third_party/openshell/patches/0005-main-raw-input.patch), [patch 6](third_party/openshell/patches/0006-main-attachments-ended.patch), [patch 7](third_party/openshell/patches/0007-main-output-backpressure.patch), [patch 8](third_party/openshell/patches/0008-cargo-ca-bundle.patch)) | [Preserved upstream LICENSE](LICENSES/OpenShell-v0.1.10-LICENSE.txt), [Apache text](LICENSES/Apache-2.0.txt) |
| NVIDIA sandbox policy | Imported policy with local changes | Apache-2.0 | Copyright 2025–2026 NVIDIA CORPORATION & AFFILIATES | [Policy and original header](assets/sandbox/sandbox-policy.rego#L1-L2) | Original header remains in the file; [Apache text](LICENSES/Apache-2.0.txt) |
| Linux VM kernel | `7.2.11` recorded build | GPL-2.0-only | Linux kernel contributors; individual source notices apply | [Exact source/configuration inputs](assets/vm/firecracker/kernel.json) | [Kernel license](assets/vm/firecracker/kernel.LICENSE), [corresponding-source requirements](assets/vm/firecracker/README.md) |
| Go dependencies | All 124 modules in the recorded `go.mod`, including two local replacements | Per-file/module terms, including MIT, BSD, Apache and MPL | Verbatim copyright statements and full texts in the inventory/bundle | [go.mod](go.mod), exact source URLs in [inventory](LICENSES/INVENTORY.json) | [Complete retained license/NOTICE texts](LICENSES/THIRD_PARTY_LICENSES.txt) |
| Rust dependencies | 929 distinct package versions across the recorded AIRC and additional reviewed OpenShell source lockfiles | Upstream Cargo license expressions, including compound terms | Verbatim copyright statements and retained full texts in the inventory/bundle | Exact crate URLs/checksums in [inventory](LICENSES/INVENTORY.json) | [Complete retained license/NOTICE texts](LICENSES/THIRD_PARTY_LICENSES.txt) |

Existing vendor licenses, component notices and NVIDIA headers are retained
unchanged. The Apache-2.0 text previously at the repository root is preserved
at [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt) for Apache components.

## Detailed inventory and preserved texts

[LICENSES/INVENTORY.json](LICENSES/INVENTORY.json) records each module/package's
name, exact version, source URL or local replacement, license evidence,
copyright statements and applicable separately named NOTICE files. The
companion [text bundle](LICENSES/THIRD_PARTY_LICENSES.txt) preserves all 1,684
supplied license/NOTICE files byte-for-byte. Identical text is stored once:
each original file has its own SHA-256, byte offset and length in the index,
covering 555 distinct payloads. All complete upstream texts remain authoritative;
classification summaries and extracted copyright lines do not replace them.

The source inventory includes optional, test and platform-only packages. Its
additional OpenShell lockfile is the reviewed upgrade source
`e8605009bed50a05136efe106a0c1e2f283ee771`, **not** the selected prebuilt
`v0.0.72` runtime's dependency inventory. This list does not assert which
packages enter a particular binary. For 56 Rust packages the reviewed archive
provided a license declaration without a separately named license/NOTICE text;
the index marks those entries, and their applicable texts/attributions must be
resolved for any distribution that includes them.

Some terms need particular care:

- `cyphar.com/go-pathrs v0.2.1` is MPL-2.0.
  `github.com/cyphar/filepath-securejoin v0.6.0` has BSD-3-Clause and MPL-2.0
  files, rather than one blanket alternative. Preserve the covered files' terms
  and provide the required covered source when distributing an incorporating
  binary.
- `gopkg.in/yaml.v3 v3.0.1` includes Apache and libyaml-derived MIT material;
  `klauspost/compress` has a multi-part license. Preserve their complete texts.
  `opencontainers/go-digest` documentation has separate CC-BY-SA-4.0 terms if
  that documentation is copied.
- Preserve applicable NOTICE text, including the collected containerd/v2,
  wazero, grpc, yaml, agext/levenshtein and moby/patternmatcher notices, plus
  component-specific hcsshim notices when those components are included.
- Rust `AND` expressions retain all applicable terms; `OR` expressions describe
  alternatives. The original upstream expressions and all supplied texts are
  retained, including Unicode/data and bundled native-code notices.

## Binary and VM distributions

This is a source notice index, not certification of final binary or VM notice
packaging. Firecracker, nerdctl/containerd/BuildKit, OpenShell, Ubuntu packages
and other runtime tools retain their own terms and transitive obligations.
For each distributed artifact, include its applicable license/copyright/NOTICE
texts and any required covered or corresponding source. In particular, the
kernel's exact source archive, configuration, recipe and license must accompany
its binary download as described in the
[kernel distribution instructions](assets/vm/firecracker/README.md).
A third-party URL alone is not a completed corresponding-source bundle.
