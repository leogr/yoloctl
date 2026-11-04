# Storage investigation tools

These standalone controls are not runtime backends. Use synthetic data in
empty disposable directories. The maintained runtime gate is documented in
[the smoke-test guide](../smoke/README.md).

## Storage contract

[storage-contract.py](storage-contract.py) tests two execution contexts that
see the same files: append/read visibility, descriptor offsets, advisory locks,
rename/lookup, shared mappings, and SQLite WAL. Coordination travels over stdio.
Known 9p mounts are refused before test writes.

```sh
python3 scripts/experiments/storage-contract.py --local-control
python3 scripts/experiments/storage-contract.py --endpoints endpoints.json \
  --report /tmp/storage-contract-result.json
```

Local control uses two local processes; it does not establish VM isolation.
An endpoints file contains exactly two objects with distinct `name` values,
an absolute `root` for the empty shared directory, and optional `prefix`
(an argv array prepended to `python3`) and `python` executable. For example,
a prefix can be `["yoloctl", "box", "exec", "probe", "--space", "trial", "--"]`.
The contexts must already exist, with Python available and no PTY translation.

Success removes synthetic files unless `--keep` is set. Failure retains them.
Existing reports are not overwritten. A pass does not establish power-loss
recovery, mapped-EOF behavior, agent login/resume, or performance. The native
account smoke phase uses this probe and adds lifecycle/persistence checks.

## Other controls

Read each script's `--help` and source before use. These tools exercise bounded
mechanisms; their outputs are not product-support claims.

| Tool | Purpose and execution environment |
|---|---|
| `dax-window-control.c` | Native Linux guarded-mapping primitive. Compile with a C compiler; uses a disposable file. |
| `kvm-dax-window-control.py` | Two tiny Linux x86_64 KVM guests; requires host `/dev/kvm`. `--prepare-only` compiles without opening KVM. |
| `kvm-writable-window-control.py` | Writable mappings across separate KVM processes. `--native-control` exercises native coordination without KVM. |
| `coordinator-lifetime-control.py` | Native-process mapping-holder lock lifetime checks. |
| [coordinated-fs](coordinated-fs/README.md) | Synthetic filesystem prototype with separate build/trial tooling; not integrated with yoloctl. |
| [firecracker](firecracker/README.md) | Isolated Linux/x86_64 fresh-boot and snapshot-restore PoC using the real controller, empty system containerd and native ext4. Requires host KVM; does not establish usable-box readiness. |
| `read-codex-quota.py` | Quota metadata reader; `--live` uses the installed CLI's app-server RPC. `--reserve-percent` selects the threshold; exit 20 means at/below it, exit 2 means unavailable. |

`guest-image.py` and `single-kernel-account.py` are incompatible with the
current space-owned account contract: they omit required creation arguments,
expect host-shared account state, and use removed cleanup flags. Their sources
are retained for inspection. Do not use them as current runtime validation.

Python helper tests can run without booting a VM:

```sh
python3 -B -m unittest discover -s scripts/experiments -v
```

Keep ignored build trees and raw trial artifacts out of repository-wide source
scans. They are local research outputs, not maintained project documentation.
