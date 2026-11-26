# Stored log views

Verification: `4580b43c30412beb0d7f57e8a4a6ef9833ef82c7`; Lima view: `e1386faff0f1db931b00d9305ec257173d38d365` and accompanying changes; Firecracker view: `1aceb626b9a21aacebda708a06e266205a2be704` and accompanying changes; box view: `61a7506a9a805e9a7b14a3256d6c6f2d24227b00` and accompanying changes; unit view: `a4322a8c233da5f9eeb51239db68fab34d55f9b0` and accompanying changes; actor and executor wording re-read at `c6c7e09c6a6aaf366d381e8adccf608c26f7f8a3`.

[FACT] `space logs` renders its existing VM-log output through the shared follow
Logger and continues using `tail -F` for follow-by-name. Partial input lines are
bounded at 64 KiB. Recognized plain prefixes supply presentation hints only;
the lossy file grammar cannot reconstruct typed outcomes or fields. Controls
are escaped in ordinary display and consecutive records fold.
`--log-level debug` preserves the original bytes on stdout. `warn`/`-q` hide ordinary/vendor text and blank
lines; `error` also hides warnings but retains uncertain results. The file
logger remains an internal full-diagnostic sink. These views never drive
lifecycle settlement.
[1](#source-1)

[FACT] For Lima's `ha.stdout.log`, the view decodes host-agent time/status
records: original timestamps, port forwarding, vsock, cloud-init and VM status.
Failed events/degradation display as warnings; status errors display as errors.
Unknown fields/types and malformed/incomplete records retain their original
text. Debug still bypasses all parsing. [2](#source-2) [3](#source-3)

[FACT] For Firecracker's `vm.log`, Firecracker v1.17 logger lines (local time,
`[instance:thread[:LEVEL…]]`) become records with actor `vm` and executor
`firecracker` at their time and level (info when the line carries none),
and kernel console lines (`[seconds] text`) records with actor `vm` and
executor `kernel`; [place labels](log-labels.md) describes how the two are
shown. yoloctl's own
records keep the plain parser. Other lines, markers included, stay generic text;
unstamped lines take the last stamped line's time (the read time before one) and serial carriage returns
are dropped for display. [4](#source-4) [5](#source-5)

[FACT] `box logs` renders the container's replayed stdout/stderr on the client
through the same follow Logger, one view per stream. OpenShell tracing lines
(`time LEVEL crate::module: msg`) become records with actor `box` and the crate
as executor, and OCSF lines (`time OCSF CLASS:ACTIVITY [SEV] msg`) records
with actor `box` and executor `ocsf`, INFO/LOW as info and MED as
warning; other severities and all other output, application text and panic
reports included, stay plain info text at the last stamped time (no prefix is read
as a level or outcome). On a terminal request only a destination that is the raw
terminal gets CRLF; debug keeps the original bytes. [6](#source-6)

[FACT] `space logs SPACE --unit NAME` reads one guest service's log from the
running VM over the space shell: `sudo tail -n N [-F]` of
`/var/log/yoloctl-units/NAME.log` in a Firecracker guest, `sudo journalctl
--no-pager -o cat -u NAME -n N [-f]` in a Lima guest. Only lines framed as
yoloctl file records (RFC3339 time, optional level, yoloctl actor) are parsed;
child output stays literal info text at the last record's time. NAME is a
managed unit (the guest's launch rule, `vmwire.ManagedUnitName`) or, for reading
only, `yoloctl-controller.service`, of letters, digits and `._@-`; anything else
is refused before the VM is contacted. A Firecracker controller inherits the
guest init's output, so its name shows the mixed VM log with a notice. It never
starts the VM or a service; debug keeps the bytes. [7](#source-7)

## Sources

1. <a id="source-1"></a> [Log view](../../cmd/yoloctl/space_log_render.go#L17) — `spaceLogOutput`, `spaceLogRecord`; `space.go` `tailLimaLog`.
2. <a id="source-2"></a> [Lima adapter](../../cmd/yoloctl/lima_space_log_render.go#L56) — `limaSpaceLogRecord`; `lima_space_log_render_test.go` source isolation, controls and debug.
3. <a id="source-3"></a> [Lima 2.2.1 event schema](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/hostagent/events/events.go) — host-agent event fields.
4. <a id="source-4"></a> [Firecracker adapter](../../cmd/yoloctl/firecracker_space_log_render.go#L41) — `newFirecrackerSpaceLogRecord`, `firecrackerRecord`; `firecracker_space_log_render_test.go` with real v1.17.0 console lines in `testdata/firecracker-vm-console.log`.
5. <a id="source-5"></a> [Firecracker v1.17.0 logger](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/src/vmm/src/logger/logging.rs) — line format, defaults and stdout target; `src/utils/src/time.rs` `LocalTime`.
6. <a id="source-6"></a> [Box log view](../../cmd/yoloctl/box_log_render.go#L37) — `newBoxLogRecord`, `viewBoxLogs`; `controller_client.go` box.logs; `box_log_render_test.go` with real OpenShell output in `testdata/box-logs-openshell.txt`.
7. <a id="source-7"></a> [Unit log view](../../cmd/yoloctl/space.go#L133) — `tailUnitLog`; `unit_log_render.go` `newUnitLogRecord`; `internal/vmservice/service.go` `LogCommand`, `logArgv`, `ErrControllerInVMLog`; `internal/vmwire/unit.go` `ManagedUnitName`; log_command_test.go, tcp_test.go `TestManagedUnitName`.
