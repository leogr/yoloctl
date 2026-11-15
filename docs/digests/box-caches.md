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

[FACT] `box cache clear NAME` is an ordinary authorized, effect-gated box
mutation under the native space and box locks. It refuses running containers
and uncertain/failed runtime observations before touching the cache. A display
"stopped" fallback is insufficient: the inventory must affirm a terminal or
never-started status, or complete absence. Missing
cache storage is a no-op. A successful clear empties and recreates the verified
leaf, reporting its allocated-byte footprint reduction; this is not a promise
of immediate global filesystem recovery. Symlink destinations, workdirs,
account state and other box leaves are untouched.
[12](#source-12) [13](#source-13)

[FACT] Only explicit `box recreate --clear-cache NAME` carries clearing intent.
Neither `--force` nor the directive-56 offer implies it. The existing lifecycle
removes the old container before creation; creation additionally verifies a
complete absent-container observation before clearing. A failed removal,
uncertain observation, cancellation or failed clear stops the replacement.
Default recreation does not perform the cache-clear observation or removal.
[14](#source-14) [15](#source-15)

[FACT] `space storage` reports each observed declared box's cache separately
from its writable layer. An exact controller-derived bind is measured at its
VM-local source; an unbound older box says "in the layer". Failed inspection or
cache traversal says "not measured" and withholds the remainder. Known separate
cache bytes are subtracted once from "guest OS and other". Cache observations
share the remaining collector budget before global walks, retaining completed
layer/cache measurements when a later read fails.
[16](#source-16) [17](#source-17)

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
6. <a id="source-6"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L2193) — existing-container start prepares the retained cache.
7. <a id="source-7"></a> [agentbox_destroy.go](../../cmd/yoloctl/agentbox_destroy.go#L1) — explicit destroy cache cleanup before declaration removal.
8. <a id="source-8"></a> [cache_test.go](../../internal/boxcache/cache_test.go#L1) — filesystem retention, ownership, links and exact removal.
9. <a id="source-9"></a> [cache_test.go](../../internal/agentbox/cache_test.go#L1) — mount assembly, overlap, policy and drift controls.

10. <a id="source-10"></a> [boxstate.go](../../internal/boxstate/boxstate.go#L453) — narrow cache conflict validation in `declaredBoxes`.
11. <a id="source-11"></a> [cache_preflight_test.go](../../cmd/yoloctl/cache_preflight_test.go#L1) — persisted recreate entry, managed pre-teardown and common runtime-preparation controls.

12. <a id="source-12"></a> [clear.go](../../internal/boxcache/clear.go#L1) — owned leaf clear, footprint measurement, cancellable traversal and private recreation.
13. <a id="source-13"></a> [box_cache.go](../../cmd/yoloctl/box_cache.go#L1) — public clear, stopped-state checks and the explicit recreate context.
14. <a id="source-14"></a> [cache_clear.go](../../internal/agentbox/cache_clear.go#L1) — command intent, fresh absence/storage checks and clearing before replacement creation.
15. <a id="source-15"></a> [box_cache_test.go](../../cmd/yoloctl/box_cache_test.go#L1) — runtime refusal, authorization, effect/native guard and non-implied flag controls.

16. <a id="source-16"></a> [space_storage.go](../../cmd/yoloctl/space_storage.go#L1) — `storageBoxCache`, native bind inspection, bounded cache observations and report partition.
17. <a id="source-17"></a> [space_storage_cache_test.go](../../cmd/yoloctl/space_storage_cache_test.go#L1) — separate sums, legacy/foreign rows, failed cache reads and completed-layer retention.
