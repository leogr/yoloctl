# Lima storage and space names

Verification: `da2a42fa3efae62ec43c0a40ac2126e5e8208a9f` and accompanying changes;
re-read cited sources when changing behavior.

[FACT] New spaces, including imports, use 1–16 ASCII characters: an
alphanumeric first character, then alphanumeric, dot, underscore or hyphen.
This applies to every VM backend. Existing space identities retain the former
1–63-character rule through loading, context selection, controller/session
references, paths and cleanup. Accounts remain 1–63 and boxes 2–63 with their
existing reserved names. The admission limit never renames existing spaces.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] On macOS, new Lima spaces use `$HOME/.config/yoloctl/lima` by default.
The host bootstrap records an absolute, clean `vm.lima_home` before VM/disk
allocation. An absent field always selects the legacy config-root `lima`
directory: normally `$HOME/Library/Application Support/yoloctl/lima` on macOS.
Existing starts/recreates never fill in the field or move storage. Config,
cache and catalog roots do not change. Explicit `YOLOCTL_HOME` and absolute
`XDG_CONFIG_HOME` overrides retain their isolated Lima directory. Linux keeps
its previous home selection and does not add the field on creation.
[1](#source-1) [2](#source-2) [4](#source-4) [12](#source-12) [14](#source-14)

[FACT] Each Lima subprocess selects its target's home in its own environment.
VM create/start/stop/delete, SSH/copy, configuration/PID paths and account-disk
inspection/allocation/blankness/deletion share that selection. Disk selection
uses the unique bootstrap declaring its full disk ID; conflicting declarations
fail. Listing inventories legacy/new/recorded roots, de-duplicates canonical
home aliases and rejects duplicate space instances or placement mismatches.
Missing recorded storage never triggers a fallback to a different home.
[2](#source-2) [5](#source-5) [6](#source-6)
[15](#source-15) [16](#source-16) [17](#source-17) [18](#source-18)

[FACT] Host mount/workdir checks exclude the legacy, new and recorded Lima
homes as well as the existing config/cache/native infrastructure exclusions.
Those checks exclude new grant requests; placement also checks previously
saved grants of every existing space before create/import publication and
allocation. Ancestor, exact and descendant overlaps refuse, including read-only
grants and stopped spaces. Existing ancestors/symlinks are resolved without
creating missing storage. A dedicated bundle inventory propagates metadata
and permission errors, including inaccessible bundles, before reading grants.
Absent bundles/manifests and readable incomplete bundles are skipped; unknown
inventories refuse placement. Display-oriented space listing is unchanged.
[22](#source-22) Valid historical
schema-2 records contribute grants to this exclusion inventory only; ordinary
schema-2 boot/declaration support remains removed. The error names the
space and grant and suggests a non-overlapping installation home. No other
space is stopped or remounted. Boot preflight and the disk-allocation seam
repeat the check, including pending-allocation recovery. New-space disk sizing
measures the destination filesystem; existing spaces measure their
recorded/legacy location.
[7](#source-7) [8](#source-8) [20](#source-20) [21](#source-21)

[FACT] The existing boot preflight remains path-dependent: resolve an existing
home's symlinks and require
`<home>/yolo-<space>/ssh.sock.1234567890123456` to be shorter than 104 bytes
on Darwin or 108 on Linux. New admission checks its destination before
publication/allocation; start and recreate check the recorded/legacy location
before boot or teardown. The error gives the endpoint, byte count, strict
limit and calculated name budget. A home too long for a one-character name
gets that explicit diagnosis. Reads and cleanup do not apply boot preflight.
[9](#source-9) [10](#source-10)

[FACT] The preflight still refuses overlong HOME/XDG/YOLOCTL_HOME paths
with names inside the 16-character cap. A deliberately shorter
real, persistent `YOLOCTL_HOME` can isolate a new installation; a symlink is
not a way around Lima's socket limit. This change provides no migration
command. Do not manually move existing VM/data directories or edit their
home mapping as an upgrade step. Existing storage remains in place.
[1](#source-1) [4](#source-4) [9](#source-9)

[FACT] The regressions use private bootstrap/VM fixtures and fake limactl
commands. They cover both home formats, longer existing names, per-command
and disk routing, conflicting homes, admission before effects and byte-boundary
preflight. These tests do not establish native macOS VM lifecycle parity.
[3](#source-3) [10](#source-10) [11](#source-11) [13](#source-13) [19](#source-19)

## Sources

1. <a id="source-1"></a> [Layout](../../internal/layout/layout.go#L55) — name validation and Lima path constructors.
2. <a id="source-2"></a> [Placement](../../internal/spaceconfig/lima_home.go#L1) — new selection, legacy fallback, disk ownership and home inventory.
3. <a id="source-3"></a> [Layout regressions](../../internal/layout/lima_home_test.go#L1).
4. <a id="source-4"></a> [Creation](../../cmd/yoloctl/controller_lifecycle.go#L257) — `createControllerSpace`.
5. <a id="source-5"></a> [Transport](../../internal/lima/home.go#L52) — paths, per-child home and shell commands.
6. <a id="source-6"></a> [Inventory](../../internal/lima/list.go#L28) — `Client.List`.
7. <a id="source-7"></a> [Host exclusions](../../cmd/yoloctl/space_lifecycle.go#L23).
8. <a id="source-8"></a> [Capacity](../../internal/vm/storage.go#L20) — `BackingAvailable`.
9. <a id="source-9"></a> [Preflight](../../internal/lima/socket_paths.go#L21).
10. <a id="source-10"></a> [CLI regressions](../../cmd/yoloctl/space_socket_paths_test.go#L1).
11. <a id="source-11"></a> [Mixed-home regressions](../../internal/lima/home_placement_test.go#L1).
12. <a id="source-12"></a> [Schema](../../internal/spaceconfig/bootstrap.go#L59) — `VMSettings`, `Validate`.
13. <a id="source-13"></a> [Bootstrap regressions](../../internal/spaceconfig/lima_home_test.go#L1).
14. <a id="source-14"></a> [Import](../../cmd/yoloctl/space_archive.go#L169) — `importSpace`, `prepareImportedSpace`.
15. <a id="source-15"></a> [VM commands](../../internal/lima/lima.go#L41).
16. <a id="source-16"></a> [Disk commands](../../internal/lima/disk.go#L20).
17. <a id="source-17"></a> [Disk blankness](../../internal/lima/disk_blank.go#L23).
18. <a id="source-18"></a> [Adapter](../../internal/vm/lima.go#L21).
19. <a id="source-19"></a> [Socket regressions](../../internal/lima/socket_paths_test.go#L1).

20. <a id="source-20"></a> [Placement exclusion](../../internal/spaceconfig/lima_placement.go#L19) — existing grants and canonical path intersection.
21. <a id="source-21"></a> [Placement regressions](../../cmd/yoloctl/lima_placement_grant_test.go#L1) — allocation, admission and pending recovery refusals, unrelated allocation control.

22. <a id="source-22"></a> [Strict inventory regressions](../../internal/spaceconfig/lima_placement_inventory_test.go#L1) — unprivileged access errors, absence/incomplete controls and no-follow refusal.
