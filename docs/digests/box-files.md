# Host recovery of box files

Verification: `ee88de4d528a26a107e3f38384423565b8d773fe` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] Host-only `box cp BOX:PATH DEST|- [--space SPACE] [--overwrite]`
exports a retained running or stopped container without starting, stopping,
reconciling or recreating it. The space and matching controller must already
run. Missing declarations, containers or required ownership evidence refuse.
`box copy` remains settings-only. Source names complete as `BOX:`; the destination
uses host file completion. [1](#source-1) [2](#source-2)

[FACT] The public command stays local; the typed `box.files.export` read has
no operator grant. Both the frontend and server refuse box/operator callers,
even with explicit `--space`. Only box/source metadata crosses the RPC; the
host destination and overwrite policy do not. Selection checks full CID,
name/space/kind labels and explicit runtime state. Native records retain
no-follow/owner/mode checks, current/generation byte equality, disk/box/
incarnation and actual binds, without current helper protocol, launcher or
readiness admission. Immediately before export the same CID is re-inspected
and verified; it alone goes to the producer. Removal fails rather than adopting
a replacement by name. No lifecycle mutation lock spans the stream. [1](#source-1)
[2](#source-2) [3](#source-3) [8](#source-8) [9](#source-9) [10](#source-10)

[FACT] The VM runs rootful `nerdctl cp -- CID:PATH -`, with argv preserved,
stdout streamed unchanged and actual producer/transport failure returned. Pinned
nerdctl resolves relative source paths from container root, follows selected
container-side links/mount mappings, and can mount a stopped snapshot. An
explicit bound path can select its existing VM source; recursively exporting a
snapshot ancestor does not collect every nested bind. No credential, account
or infrastructure path denylist is applied. Running files are best-effort live
reads, not a consistent snapshot. [4](#source-4) [5](#source-5) [11](#source-11)

[FACT] An existing host directory receives the archive's selected basename;
a directory copied to a missing DEST becomes DEST. Source `.` or a final `/.` component (including a trailing slash) merges its
contents. Filesystem extraction opens the existing destination parent before
receiving, holds its directory authority, and stages privately beside DEST.
Every tar entry and successful producer/transport completion precedes
publication. Staging needs space proportional to the contents. Default
publication never replaces a racing entry. Existing directories merge; a later
failure explicitly reports a partially published prefix. `--overwrite` replaces
regular files atomically and never follows a destination link or changes a
file into a directory. Parent paths containing ordinary symlinks refuse;
macOS's system `/tmp`, `/var`, `/etc` aliases are normalized. [6](#source-6)
[7](#source-7)

[FACT] Extracted files belong to the host caller and retain their staging
inode's group after rename. The OS may inherit the staging parent's group,
notably on macOS; an existing final destination's setgid group is not adopted.
Ordinary file/directory mode and mtime survive; VM UID/GID,
setid bits, capabilities and xattrs do not. Absolute/traversal/duplicate names,
link parents, escaping or cyclic symlinks, unsupported special entries and
invalid hardlinks refuse before publication. Relative links stay within the
selected tree; hardlinks target validated regular files. This is constrained
file recovery, not complete Docker/nerdctl archive extraction parity. [6](#source-6)
[7](#source-7)

[FACT] DEST `-` emits only the upstream uncompressed tar, with diagnostics on
stderr; no host extraction rules alter that archive. On failure or cancellation
it can be partial and exits nonzero. Filesystem destinations publish nothing
until the stream succeeds; cancellation joins the exact producer before owned
staging cleanup. Existing-directory merges are not transactions. No automatic
retry occurs. [1](#source-1) [6](#source-6)

## Sources

1. <a id="source-1"></a> [CLI and typed request](../../cmd/yoloctl/agentbox_cp.go#L21-L187) — `newAgentboxCPCmd`, `boxCPHostAllowed`, `decodeBoxFilesRequest`, `completeBoxCP`.
2. <a id="source-2"></a> [Exact container selection](../../cmd/yoloctl/agentbox_cp_worker.go#L19-L90) — `runControllerBoxFiles`, `checkBoxFileContainer`, `exportRetainedBox`.
3. <a id="source-3"></a> [Retained native ownership](../../internal/agentbox/file_export_linux.go#L20-L119) — `VerifyFileExportRuntime`, `verifyFileExportRecords`, `validateFileExportRecord`; reuses `openshell_native_linux.go` `readRuntimeMetadata`.
4. <a id="source-4"></a> [Producer](../../internal/container/copy.go#L12-L26) — `CopyArchive`.
5. <a id="source-5"></a> [nerdctl v2.3.5 stopped snapshot and tar arguments](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/containerutil/cp_linux.go) — `CopyFiles`, container-to-stdout tar branches.
6. <a id="source-6"></a> [Archive validation and publication](../../internal/boxexport/export.go#L43-L467) — `Copy`, `receive`, `publishEntry`, `checkLink`.
7. <a id="source-7"></a> [Host directory authority](../../internal/boxexport/fs_unix.go#L32-L218) — `openHostDirectory`, `newStage`, `moveEntry`, `cleanupStage`; platform `renameExclusive` uses Linux `RENAME_NOREPLACE` / Darwin `RENAME_EXCL`.
8. <a id="source-8"></a> [Host-only registry](../../cmd/yoloctl/controller_internal.go#L38-L55) — `controllerOperations`; `executeControllerWorker` dispatches the typed read.
9. <a id="source-9"></a> [Server authorization](../../internal/controller/protocol.go#L231-L266) — `Authorize`, `BoxAllowed`.
10. <a id="source-10"></a> [Export-only record decoder](../../internal/openshell/export_record_linux.go#L10-L49) — `DecodeExportRecord`.
11. <a id="source-11"></a> [nerdctl v2.3.5 source resolution](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/containerutil/cp_resolve_linux.go) — relative-root, source-link and mount mapping.
