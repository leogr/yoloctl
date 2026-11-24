# VM image-preparation reserve notices

Verification: `1ea4713a87d08ef6ef50528cde2aff060bcde72b` and accompanying changes.

[FACT] Human `box create`, `box recreate`, `box start` and `box list` requests
observe the VM root filesystem once after worker authorization, before image
warming or apply. Below image preflight's effective capped admission reserve they emit one
stderr warning with the measured free/required GiB and `yoloctl space storage`
guidance. Statfs failure, unknown capacity and sufficient free space emit no
notice. This does not start cleanup, walk files, refuse an operation or change
its result. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The requirement is min(20% of disk, `storage.build_reserve_max`), default
10 GiB. `none` restores 20%; `0GiB` requires no reserve. An unreadable setting
uses uncapped 20%, disclosed in the warning. Cleanup/view thresholds remain
unchanged. [10](#source-10) [11](#source-11)

[FACT] Successful `space start` observes the same notice with the existing
admin-only `control.ping`, using internal request metadata. A failed optional
read is ignored. Ordinary pings keep their existing output and authorization;
operators cannot acquire this administration read. The warning is rendered
through the normal CLI logger. [1](#source-1) [4](#source-4) [5](#source-5)

[FACT] JSON and internal inventory/completion reads emit no new warning.
Human all-space box lists use the admin observation and emit at most one
notice, naming its space. The space view's existing disk thresholds and
events are unchanged. There is no new margin, quota or cleanup ownership.
[1](#source-1) [6](#source-6) [7](#source-7) [8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [storage_warning.go](../../cmd/yoloctl/storage_warning.go#L1) — `diskReserveWarning`, request filtering and optional startup read.
2. <a id="source-2"></a> [policy.go](../../internal/storagegc/policy.go#L17) — root statfs, `Low` and `TargetFree`.
3. <a id="source-3"></a> [controller_internal.go](../../cmd/yoloctl/controller_internal.go#L548-L551) — warning after authorization, outside warming retries.
4. <a id="source-4"></a> [controller_lifecycle.go](../../cmd/yoloctl/controller_lifecycle.go#L174-L189) — successful host start observation.
5. <a id="source-5"></a> [server.go](../../internal/controller/server.go#L309) — admin ping and opt-in result.
6. <a id="source-6"></a> [controller_views.go](../../cmd/yoloctl/controller_views.go#L63) — human all-space list, JSON suppression and one notice.
7. <a id="source-7"></a> [storage_warning_test.go](../../cmd/yoloctl/storage_warning_test.go#L1) — threshold, unknown read, JSON and optional host read controls.
8. <a id="source-8"></a> [Ping controls](../../internal/controller/storage_warning_test.go#L1) — ordinary output, operator refusal and maintenance.
9. <a id="source-9"></a> [disk.go](../../internal/spacetelemetry/disk.go#L17) — unchanged view pressure thresholds.
10. <a id="source-10"></a> [reserve.go](../../internal/storagegc/reserve.go#L1) — default, parsing and admission formula.
11. <a id="source-11"></a> [build_reserve.go](../../cmd/yoloctl/build_reserve.go#L1) — native setting read and disclosed fallback.
