# Configurable image-build admission reserve

Verification: `1ea4713a87d08ef6ef50528cde2aff060bcde72b` and accompanying changes; reread cited code when changing behavior.

[FACT] Build/pull preflight's final free-space test requires
`min(Disk.TargetFree(), cap)`, with `TargetFree = total/5`. The default cap is
10 GiB. The reserve does not change `Low`, `TargetFree`, `CacheBudget`, idle
cleanup or BuildKit's GC targets. Low space still triggers cleanup, even when
the capped final admission would succeed; existing cleanup errors, its 30 s
budget, parent cancellation and reclamation wait remain. `0GiB` bypasses only
the final free-space refusal. It is not a quota or a limit on running writers.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] `storage.build_reserve_max` belongs to controller-owned native settings
in `config/space.yaml`, not the host bootstrap. Omission means the default;
`none` means uncapped 20%; a whole GiB string, including `0GiB`, sets the cap.
Fractions, other units, malformed sizes and byte overflow are rejected. The
three states survive config save/load and split/compose. Existing spaces with
no setting use the default. [1](#source-1) [4](#source-4) [5](#source-5)

```sh
yoloctl space storage set dev --build-reserve-max 12GiB
yoloctl space storage set dev --build-reserve-max none
yoloctl space storage set dev --build-reserve-max 0GiB
```

[FACT] The setter requires a matching running controller. Its host-only
`space.storage.set` RPC validates its typed input, takes the native space
mutation lock and checkpoints whole-space effects before saving atomically.
Box operators are refused. It changes no boot authority, starts no VM and
needs no restart. A post-save unavailable statfs observation is reported as
unavailable, while the setting remains saved. [6](#source-6) [7](#source-7)

[FACT] Each native preflight reads the setting once. Read/validation failure
uses the previous uncapped 20% rule, logs that fallback and names it in any
refusal. A previously capped space can consequently be refused until the read
works again. Optional human notices use the same effective requirement.
`space storage` prints it and emits `build_reserve: {required, fraction_free,
cap}` in JSON (byte counts; `cap: null` means uncapped). Unknown disk capacity
leaves this observation null; failed settings reads have an explicit error.
Measurements and C's partial-result budget/unknown rules remain unchanged.
[2](#source-2) [8](#source-8) [9](#source-9) [10](#source-10)

[FACT] Fixture tests cover default/none/zero/size round trips, boundary
admission, unchanged cleanup targets/errors/cancellation, disclosed fallback,
effective warnings/reporting and guarded native-setting persistence. These
are source controls, not live VM or macOS validation. [11](#source-11)
[12](#source-12) [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [reserve.go](../../internal/storagegc/reserve.go#L1) — parser, `AdmissionFree`, `Admits`.
2. <a id="source-2"></a> [storage_gc.go](../../cmd/yoloctl/storage_gc.go#L165) — native reserve read and `beforeBuild`.
3. <a id="source-3"></a> [policy.go](../../internal/storagegc/policy.go#L31) — unchanged cleanup targets.
4. <a id="source-4"></a> [storage.go](../../internal/spaceconfig/storage.go#L1) — native setting and parser.
5. <a id="source-5"></a> [native.go](../../internal/spaceconfig/native.go#L1) — load/save; `Settings` split/compose in `bootstrap.go`.
6. <a id="source-6"></a> [space_storage_set.go](../../cmd/yoloctl/space_storage_set.go#L1) — host CLI and native mutation handler.
7. <a id="source-7"></a> [controller_internal.go](../../cmd/yoloctl/controller_internal.go#L119) — host-only gated registration and dispatch.
8. <a id="source-8"></a> [build_reserve.go](../../cmd/yoloctl/build_reserve.go#L1) — observation, formatting and fallback.
9. <a id="source-9"></a> [storage_warning.go](../../cmd/yoloctl/storage_warning.go#L1) — effective threshold and preserved filtering.
10. <a id="source-10"></a> [space_storage.go](../../cmd/yoloctl/space_storage.go#L80) — optional reserve source, JSON and human output.
11. <a id="source-11"></a> [reserve_test.go](../../internal/storagegc/reserve_test.go#L1) — boundary/parser/cleanup invariants.
12. <a id="source-12"></a> [storage_test.go](../../internal/spaceconfig/storage_test.go#L1) — distinct persistence and bootstrap separation.
13. <a id="source-13"></a> [build_reserve_test.go](../../cmd/yoloctl/build_reserve_test.go#L1) — preflight cancellation/fallback and warning/report controls.
14. <a id="source-14"></a> [space_storage_set_test.go](../../cmd/yoloctl/space_storage_set_test.go#L1) — actual native save and effect/authorization boundary.
