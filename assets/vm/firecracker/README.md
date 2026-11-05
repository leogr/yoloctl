# Bundled Firecracker kernel

`kernel-linux-amd64.gz` is a gzip-compressed Linux ELF kernel, embedded only in
Linux amd64 host builds. It is an infrastructure component; the VM root image
is still assembled locally on first use. The kernel runs inside the VM, not as
a host executable. Normal `make build` requires Go, not a kernel toolchain.

`kernel.json` records the exact source/configuration URLs and SHA256s, enabled
and disabled configuration changes, compiler/tool identities and output hashes.
`kernel.config` is the complete effective configuration. `inputs.py:kernel`
is the maintainer build recipe; `scripts/vm/firecracker/linux7.py kernel`
invokes it. After building and reviewing an intentional kernel update, run:

```sh
python3 scripts/vm/firecracker/bundle-kernel.py --kernel-build PATH
```

The updater verifies the source pins, options and output hashes before replacing
the compressed artifact, configuration and manifest. Gzip metadata is fixed;
the recipe fixes build metadata. `--reproduce` builds in two independent object
directories and compares kernel/config bytes; this does not claim identical
output across toolchain versions. Run native assembly and an
isolated live VM smoke after an update; configuration checks alone do not prove
OpenShell confinement or runtime compatibility.

The audit configuration enables built-in BTF, BPF JIT and tracing. GCC 13.3.0
and pahole 1.25 are required. `kernel-tools.json` pins the arm64-host/x86_64-target
qualification packages by URL/version/SHA256. They can be extracted into an
owned cache without installing packages or changing host tool configuration. The
recipe accepts `--cc` and `--cross-compile` for this offline cross-build. Native
x86_64 compilation remains supported. BTF is retained when standalone DWARF
sections are stripped after encoding. The recipe maps C/assembly debug paths
and encodes BTF serially to avoid pahole 1.25 parallel type-ID variation.
Configuration and ELF checks do not prove
a live Falco probe load.

The kernel is separately licensed under GPL-2.0; see `kernel.LICENSE`. Complete
corresponding upstream source is identified by the versioned source archive and checksum
in `kernel.json`. The recipe changes configuration, not kernel source. When
distributing bundles, provide that source archive, configuration, recipe and
license alongside the binary download. This file is not a substitute for
providing corresponding source with a release.

`build.py` remains a tool-based maintainer image builder for the experiment
harness. It is not embedded or called by the installed CLI. The production
assembly implementation is `internal/firecracker/assemble_linux.go`.
