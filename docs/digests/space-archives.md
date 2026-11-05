# Space archives and restore

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying fixes; archive-path expansion `1d265df3` and accompanying changes; grow guard `1d265df3cfae49026ec4202fe88e9cce83ad1ca9`; native links `702c6ac7087f9c18a86edb0427fd4349cd82e788` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Host-only `space export SPACE ARCHIVE.tar.gz` stores a version-1
manifest and `state.tar` in gzip. The manifest records source boot settings,
payload size and SHA256. Native payloads contain declarations, account files and
declared managed chat server data;
VM disks, container/image caches, sessions, ephemeral workdirs, host projects,
global catalogs and old controller receipts are excluded. Archives contain
account secrets in plaintext. Output is private (0600), flushed and published
without replacing an existing path. Both export and import expand a leading
literal `~/` in the archive path to the host user's home directory. [1](#source-1) [2](#source-2)

[FACT] Export takes the host lifecycle lock, requires the selected controller
build, and temporarily starts a stopped VM. It drains admission and requires
stopped workloads; `--stop` explicitly stops consumers through shared teardown.
The native exporter takes the VM operation lock and verifies native storage.
Export refuses while a [data disk grow](data-disk-grow.md) is pending, since
its temporary start would perform it [17](#source-17).
A previously running controller resumes admission without restarting workers;
a previously stopped VM is stopped afterward. Boxes are never automatically
restarted. Resume cannot clear uncertain receipts or a receipt-storage failure.
[1](#source-1) [3](#source-3) [4](#source-4)

[FACT] `space import SPACE ARCHIVE.tar.gz` refuses an existing destination.
It validates the complete compressed stream, bounded inventory, declarations,
size and checksum before allocating a VM. Backend selection defaults to Firecracker on Linux amd64 and Lima elsewhere;
`--backend` explicitly selects the destination adapter. Import creates fresh disk, space
and generation identities. Box declarations are renamed to the destination;
accounts retain their names and native contents. Boxes stay stopped and their
runtime is rebuilt on explicit start. [1](#source-1) [2](#source-2)

[FACT] Host grants are reused from the archive unless remapped with `--workdir`
or repeated `--mount-map OLD=NEW`. Destination directories must already exist;
changed host aliases, managed-storage intersections and box binds exceeding
grants are rejected. Destination global catalogs must resolve imported boxes;
archives do not install or overwrite global catalog definitions. Arbitrary
strings inside agent files, environment values and `session_command` argv are
not rewritten. AIRC feature selections and literal agentless session arguments
survive backend/workdir remapping. [1](#source-1) [10](#source-10)

[FACT] Export translates clean absolute symlinks beneath the same native leaf,
its VM bind alias, the profile's `state_container_path`, or an additional
`state_mounts` view into relative links within the same leaf.
It never follows targets or changes source files. Other clean absolute symlink
targets are retained verbatim, including dangling targets; their contents are
not archived. Unclean absolute targets fail with the entry name. Unix socket
inodes are omitted. Optional profile
`state_archive_excludes` names literal relative paths/subtrees, never globs or
the leaf root. Codex declares `tmp/arg0` and `tmp/arg0-linux`, its control socket path and four daemon
PID files. Settings, logs, saved history, credentials and installed releases
remain. Other profiles retain ordinary files unless explicitly excluded.
[11](#source-11) [12](#source-12) [13](#source-13) [14](#source-14)

[FACT] Native account regular files preserve bytes, ordinary permission bits and
modification times; directory modes/times, relative in-leaf symlinks (including
links to the leaf root), and hardlinks are retained. Files/directories use the
importing developer's UID/GID; account roots stay private to root. Import still
rejects absolute/escaping hardlinks, relative symlinks escaping the leaf, unclean
absolute symlinks, special files, duplicate entries, missing/link
parents and unknown accounts. Export rejects non-socket special files too.
Extraction stages beneath a verified native disk, validates all payload bytes,
then publishes complete account directories. Extended attributes/ACLs are not
stored. [5](#source-5)

[FACT] Managed chat archives retain desired membership and server data, excluding
generated config, credentials and endpoints. Import remaps room/member/box IDs
within the fresh disk scope and generates new native credentials. Only private
regular files/directories are accepted for chat data; links and missing roots
fail. Differing partial data is retained for inspection. The state foundation
does not apply live membership or start a server. [15](#source-15) [16](#source-16)

[FACT] A private host initialization seed and checked `import-state.tar` remain
until native restore and controller activation complete. A native completion
marker identifies the payload. Explicit retry accepts identical previously
published directories; differing partial state fails closed. Completed restore
never overwrites later account changes. No existing disk is reformatted and no
ordinary command reads declarations from archive/recovery files.
[1](#source-1) [3](#source-3) [5](#source-5) [6](#source-6)

[FACT] The old schema-2 migration command is removed. Unsupported schema-2
records give a previous-build recovery hint. The old optional migration field
is accepted only for reading existing schema-3 bootstrap records.
[6](#source-6) [7](#source-7)

[FACT] Unit tests cover Codex/custom-profile artifacts, unchanged source state,
link boundaries, checksum/CRC failures and completed-import retry. The isolated
archive smoke checks rejected-link diagnostics, portable daemon links and runtime
exclusions as well as CLI export/import, remapping, new filesystem identity, recreate
and stopped-space export; `SMOKE_IMPORT_BACKEND` selects cross-adapter restore; its existence does not establish a host runtime pass.
[8](#source-8) [9](#source-9) [12](#source-12)

## Sources

1. <a id="source-1"></a> [Host archive lifecycle](../../cmd/yoloctl/space_archive.go#L1) — command contracts, export/import, publication and remapping.
2. <a id="source-2"></a> [Archive envelope](../../internal/spacearchive/archive.go#L1) — `Manifest`, `Read`, `Write`, bounded trailer verification.
3. <a id="source-3"></a> [Native archive entry points](../../cmd/yoloctl/space_archive_internal.go#L1) — `restoreNativeSpace`, `exportNativeSpace` and native locks.
4. <a id="source-4"></a> [Controller admission](../../internal/controller/server.go#L1) — `Drain`, `Resume`, host-only control methods.
5. <a id="source-5"></a> [Native inventory and publication](../../internal/spacearchive/state.go#L1) — `WriteState`, `inspectState`, `RestoreState`.
6. <a id="source-6"></a> [Host lifecycle](../../cmd/yoloctl/controller_lifecycle.go#L1) — initialization seeds, explicit start and seed retirement.
7. <a id="source-7"></a> [Bootstrap](../../internal/spaceconfig/bootstrap.go#L1) — `ImportState`, `ErrLegacySchema`, strict decoding.
8. <a id="source-8"></a> [Archive regressions](../../internal/spacearchive/archive_test.go#L1) — round trip and unsafe archive tests.
9. <a id="source-9"></a> [Archive runtime gate](../../scripts/smoke/space-archive.sh#L1) — isolated export, independent restore and lifecycle checks.
10. <a id="source-10"></a> [Import mapping regression](../../cmd/yoloctl/space_archive_test.go#L53) — canonical grants and a symlink destination exercise Firecracker-to-Lima mapping, preserving AIRC features and the resolved session argv.
11. <a id="source-11"></a> [Native export policy](../../internal/spacearchive/native_export.go#L17) — `nativeExportPolicy.skip`, `relativeLink`; bounded runtime exclusions and portable targets.
12. <a id="source-12"></a> [Native export regressions](../../internal/spacearchive/native_export_test.go#L63) — Codex and custom-profile round trips, original bytes/links retained and unsafe targets rejected by name.
13. <a id="source-13"></a> [Codex profile](../../internal/agentprofile/builtin/codex.yaml#L43) — `state_archive_excludes` identifies disposable process state.
14. <a id="source-14"></a> [Profile validation](../../internal/agentprofile/profile.go#L384) — `Profile.Validate` rejects unsafe exclusion paths.
15. <a id="source-15"></a> [Managed chat archive implementation](../../internal/spacearchive/chat.go#L20) — `writeChatState`, `validateChatEntry`, `restoreChatState`.
16. <a id="source-16"></a> [Managed chat archive regressions](../../internal/spacearchive/chat_test.go#L1) — independent identities, interrupted restore and unsafe inventories.
17. <a id="source-17"></a> [Export grow guard](../../cmd/yoloctl/space_archive.go#L79-L81) — the `DataDiskGrow` refusal in `exportSpace`.
