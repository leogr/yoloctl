# Synthetic filesystem prototype

This is a standalone experiment, not a yoloctl backend. Use only disposable
synthetic data; it is not qualified for real accounts, credentials or workdirs.
The code and [pins.json](pins.json) define the tested mechanisms and dependencies.
There is no production compatibility or performance claim.

## Build and run

The build script requires Linux x86_64 with 4 KiB pages, Python with
`hashlib.file_digest` and tar extraction filters, Rust/Cargo, a C compiler,
make, patch, bison, flex, bc, m4 and pkg-config. Building its kernel/runtime also
needs the corresponding libc and kernel build dependencies. It downloads
pinned archives and checks SHA-256, or accepts them through `--cache`.

```sh
python3 scripts/experiments/coordinated-fs/build.py --directory /var/tmp/ycf-build
python3 scripts/experiments/coordinated-fs/trial.py --build /var/tmp/ycf-build
```

The build directory must be new. Trial execution additionally needs host
`/dev/kvm`, bubblewrap and unprivileged user namespaces; it refuses 9p backing.
It creates isolated guest roots and records logs/reports beneath a fresh
`/var/tmp/ycf-trial-*` directory. It does not operate existing Lima spaces.
`--refresh-launcher` recompiles the small launcher into the trial directory
when its source differs from the build report, without rebuilding the kernel.

`followup.py --build /var/tmp/ycf-build` runs mapped-resize, coordinator-loss,
stopped-peer and large-append controls using fresh synthetic guests. A completed
report can include observed limitations; read the individual results.

## Component checks

```sh
python3 -B -m unittest discover -s scripts/experiments/coordinated-fs -p 'test_*.py' -v
```

The build also runs native Rust/mapping controls. Native tests do not establish
Linux guest filesystem semantics or validate an OpenShell runtime. Preserve
reports with their recorded source hashes when investigating a result.
