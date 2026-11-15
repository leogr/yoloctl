# Per-box cache directories

Verification: `94ddab7293b86cb7740751dc588bed751c7e92f2` and accompanying changes.

[FACT] New confined containers created by the native controller bind one
VM-local directory, `/var/lib/yoloctl/box-cache/BOX`, read-write at
`/home/sandbox/.cache`. The controller creates the parent root-owned mode0755
and the box leaf with the space user's UID/GID, mode0700. Existing links,
foreign owners and unexpected modes are refused rather than repaired.
These directories live on the VM system disk, not the account/data disk.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The bind is controller-derived infrastructure, kept in `CacheMounts`
separately from declared mounts. Its absence in an older container is not
recreation drift: that container's caches remain in its writable layer.
A declared mount at the cache target, below it or on an ancestor is invalid
for a confined box; remove or change that mount before create/recreate.
Guest destinations are normalized before overlap checks. Persisted conflicts
are reported as `declaration_invalid` by the shared resolver; manifests stay
readable for explicit stop/destroy recovery. Create/recreate qualification
rejects the conflict before image/runtime preparation or old-runtime teardown.
[2](#source-2) [4](#source-4) [5](#source-5) [10](#source-10) [11](#source-11)

[FACT] Stop, start, restart and ordinary recreate preserve the directory.
A subsequent start verifies the bound source before launching; an unbound
older container is left alone. Destroy removes only the named owned cache
leaf after container removal, without following links into other storage.
Missing cache state is harmless. Automatic image/BuildKit cleanup never
collects these directories. Privileged containers do not acquire this bind.
[1](#source-1) [2](#source-2) [6](#source-6) [7](#source-7)

[FACT] Tests cover retained contents, exact-leaf deletion, foreign root/leaf
rejection, symlink targets surviving removal, declared target overlap,
unchanged policy bytes and no recreation drift with or without the bind.
They do not establish a live VM result or enforce tool cache locations:
a process can still write outside `.cache`.
[8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [cache.go](../../internal/boxcache/cache.go#L1) — `Ensure`, `Remove`, descriptor-relative parent/leaf validation.
2. <a id="source-2"></a> [cache.go](../../internal/agentbox/cache.go#L1) — cache option assembly, overlap and retained-bind checks.
3. <a id="source-3"></a> [openshell_create_linux.go](../../internal/agentbox/openshell_create_linux.go#L1) — native creation adds the cache bind after image/storage qualification.
4. <a id="source-4"></a> [live.go](../../internal/agentbox/live.go#L1) — `LiveInspector` separates cache mounts, `Create` shares cache assembly.
5. <a id="source-5"></a> [agentbox.go](../../internal/agentbox/agentbox.go#L504) — `Config.Validate` rejects overlapping declarations.
6. <a id="source-6"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L2189) — existing-container start prepares the retained cache.
7. <a id="source-7"></a> [agentbox_destroy.go](../../cmd/yoloctl/agentbox_destroy.go#L1) — explicit destroy cache cleanup before declaration removal.
8. <a id="source-8"></a> [cache_test.go](../../internal/boxcache/cache_test.go#L1) — filesystem retention, ownership, links and exact removal.
9. <a id="source-9"></a> [cache_test.go](../../internal/agentbox/cache_test.go#L1) — mount assembly, overlap, policy and drift controls.

10. <a id="source-10"></a> [boxstate.go](../../internal/boxstate/boxstate.go#L453) — narrow cache conflict validation in `declaredBoxes`.
11. <a id="source-11"></a> [cache_preflight_test.go](../../cmd/yoloctl/cache_preflight_test.go#L1) — persisted recreate entry, managed pre-teardown and common runtime-preparation controls.
