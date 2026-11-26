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
| OpenShell runtime (legacy) | `v0.0.72`; no longer provisioned by new VM images after the 0.1.6 upgrade, present only in VM images built before it | Apache-2.0. Dependencies retain their terms | Upstream copyright holders; see upstream source and retained headers | [Pinned VM inputs](assets/vm/firecracker/pins.json), [upstream](https://github.com/NVIDIA/OpenShell/tree/v0.0.72) | [Preserved upstream LICENSE](LICENSES/OpenShell-v0.0.72-LICENSE.txt), [Apache text](LICENSES/Apache-2.0.txt) |
| OpenShell runtime | `v0.1.10` from upstream commit `152c59d3178039e3fbb4a5595f9fff7216a6be92` plus eight yoloctl patches (final source tree `9e310ff9a8f58c80409f2d9b2802e9fc6781130b`), embedded for Linux amd64 and arm64. Existing boxes retain their recorded runtime until recreated. | Apache-2.0. Dependencies retain their terms | Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. See upstream headers for other holders | [Pinned source and patch trees](third_party/openshell/pins.json), [upstream source](https://github.com/ai-openshell/openshell/tree/152c59d3178039e3fbb4a5595f9fff7216a6be92) | [Preserved upstream LICENSE](LICENSES/OpenShell-v0.1.10-LICENSE.txt), [Apache text](LICENSES/Apache-2.0.txt) |
| Mermaid renderer | Embedded WASM built from `mermaid-text` 0.58.0 | MIT and component-specific terms | Copyright 2026 leboiko and the holders named in the retained texts | [Pinned renderer and inputs](third_party/mermaid-text/pins.json) | [Renderer LICENSE](third_party/mermaid-text/LICENSE), [dependency manifest](third_party/mermaid-text/licenses/manifest.json), [Rust library notice](third_party/mermaid-text/licenses/rust-1.95.0/COPYRIGHT-library.html) |
| Falco extraction plugin | Embedded Linux amd64 and arm64 plugin with Falco C ABI headers from libs 0.26.1 and static C++ runtime | MIT for yoloctl code, Apache-2.0 for the Falco headers, and runtime-specific terms | Copyright and attribution in the retained notices | [Plugin source and provenance](third_party/audit-plugin/README.md) | [Embedded plugin notices](internal/auditplugin/embedded/NOTICES.txt) |
| NVIDIA sandbox policy | Imported policy with local changes | Apache-2.0 | Copyright 2025–2026 NVIDIA CORPORATION & AFFILIATES | [Policy and original header](assets/sandbox/sandbox-policy.rego#L1-L2) | Original header remains in the file; [Apache text](LICENSES/Apache-2.0.txt) |
| Linux VM kernel | `7.3.6` recorded build | GPL-2.0-only | Linux kernel contributors; individual source notices apply | [Exact source/configuration inputs](assets/vm/firecracker/kernel.json) | [Kernel license](assets/vm/firecracker/kernel.LICENSE), [corresponding-source requirements](assets/vm/firecracker/README.md) |
| Go dependencies | All 121 modules in the recorded `go.mod`, including two local replacements | Per-file/module terms, including MIT, BSD, Apache and MPL | Verbatim copyright statements and full texts in the inventory/bundle | [go.mod](go.mod), exact source URLs in [inventory](LICENSES/INVENTORY.json) | [Complete retained license/NOTICE texts](LICENSES/THIRD_PARTY_LICENSES.txt) |
| Rust dependencies | 993 distinct registry package versions across the pinned AIRC and selected OpenShell lockfiles, plus 3 packages from the pinned OpenShell Cedar fork | Upstream Cargo license expressions, including compound terms | Verbatim copyright statements and full texts in the inventory/bundle | Exact crate URLs, checksums and Git revision in [inventory](LICENSES/INVENTORY.json) | [Complete retained license/NOTICE texts](LICENSES/THIRD_PARTY_LICENSES.txt) |

Existing vendor licenses, component notices and NVIDIA headers are retained
unchanged. The Apache-2.0 text previously at the repository root is preserved
at [LICENSES/Apache-2.0.txt](LICENSES/Apache-2.0.txt) for Apache components.

## Detailed inventory and preserved texts

[LICENSES/INVENTORY.json](LICENSES/INVENTORY.json) records each module/package's
name, exact version, source URL or local replacement, license evidence,
copyright statements and applicable separately named NOTICE files. The
companion [text bundle](LICENSES/THIRD_PARTY_LICENSES.txt) preserves all 1,783
supplied license/NOTICE files byte-for-byte. Identical text is stored once:
each original file has its own SHA-256, byte offset and length in the index,
covering 581 distinct payloads. All complete upstream texts remain authoritative;
classification summaries and extracted copyright lines do not replace them.

The source inventory includes optional, test and platform-only packages.
The OpenShell lockfile is the stamped build input for the selected v0.1.10
runtime after applying yoloctl's pinned patches. The AIRC lockfile remains
at its pinned v0.1.3 revision. This list does not assert which packages
enter a particular binary. For 64 registry crates the reviewed archive
provided a license declaration without a separately named license/NOTICE
text. The index marks those entries so any applicable texts and
attributions can be resolved for a distribution that includes them.

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
