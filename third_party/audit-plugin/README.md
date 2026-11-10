# Falco extraction plugin

The small yoloctl-owned C++ plugin enriches live syscall events from a private
controller observation snapshot. It exports six `yolo.*` metadata fields and
`yolo.metadata_available`. It does not grant authority or prove per-process
confinement. See the [metadata digest](../../docs/digests/audit-metadata.md).

The plugin explicitly exports required event schema `4.0.0`, independently of
API `3.12.0`. It reads the common event TID and the foreign container state
table, without parsing minor-version-specific syscall parameters. Without the
schema export the framework assumes `3.0.0`, which is incompatible with a
4.x engine even when `-V` accepts the rules.

The original C ABI headers are from `falcosecurity/libs` tag `0.26.1`, API
`3.12.0`, matching the libs inside pinned Falco 0.45.2; [pins](include/pins.json) record their exact bytes. Their Apache-2.0
license and notices are retained in [LICENSE](LICENSE) and the headers. The
plugin's own implementation uses the repository's MIT license. Static C++
runtime notices from Zig 0.15.2 are in [runtime-licenses](runtime-licenses/).
The generated notices are embedded with the plugin artifacts in ordinary Go
binaries; no C++ runtime shared library is needed in the guest.

Ordinary Go builds embed the checked-in Linux amd64/arm64 shared objects and
manifest in `internal/auditplugin/embedded`. Maintenance uses the same Zig
`0.15.2` pin as the native CI toolchain, targeting glibc 2.28:

```sh
python3 scripts/build-audit-plugin.py --zig /absolute/path/to/zig
python3 scripts/build-audit-plugin.py --check --zig /absolute/path/to/zig
```

The checker builds both targets twice using separate compiler caches and
compares their bytes and manifest with the tracked artifacts. Linux hosts also
execute parser/C ABI fixtures, including the shared Go/C++ snapshot golden,
and dynamically load the actual native DSO to verify its schema/API/version.
A cross-build and synthetic ABI fixture do not establish native amd64 Falco or
live BPF/containerd behavior. The opt-in Go archive test validates rules and opens a no-driver engine with
`-M 1`; no-driver mode alone does not check the real engine's schema admission.
On an authorized native Linux executor with BTF and BPF/perf privileges, require
the real modern engine (no fallback or permission-error skip):

```sh
FALCO_TEST_ARCHIVES=/absolute/qualified-tarballs FALCO_TEST_ENGINE=modern_ebpf \
  go test -v -count=1 ./internal/audit -run '^TestPinnedArchivesInstallVerifyAndRuleSyntax$'
```

The disposable host gate still owns real events and identity/noise qualification.

Change the source/headers/recipe, regenerate, check reproducibility, run the Go
artifact/protocol and repository gates, and update digest facts and citations.
Never hand-edit a qualified shared object or manifest hash. The plugin reads a
fixed private controller file, with no network or per-event RPC.
