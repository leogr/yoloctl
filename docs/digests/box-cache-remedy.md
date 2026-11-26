# Reclaiming tool caches in a box

Verification: `a50eee6dde4e277ddc3450a4762cd2c85a3dfd17` and accompanying changes; separate-cache commands on the accompanying changes to main `1ea4713a`; tool documentation checked 2026-11-15.

[FACT] `space storage` identifies measured writable layers; its help offers
explicit cache remedies using the existing `box exec BOX -- COMMAND` route.
Run only tools installed in that box with cache access permitted by its policy.
[1](#source-1) [2](#source-2)

```sh
yoloctl box exec BOX -- go clean -cache
yoloctl box exec BOX -- go clean -modcache
yoloctl box exec BOX -- npm cache clean --force
yoloctl box exec BOX -- python3 -m pip cache purge
```

[FACT] Go's first command clears build cache; the second clears downloaded
modules. npm's command clears its cache and requires `--force`; pip's purge
removes cached items. These caches may be rebuilt/downloaded on the next use.
They are explicit user actions, never automatic cleanup of box data.
[3](#source-3) [4](#source-4) [5](#source-5)

[FACT] `box recreate BOX` removes the container and replaces it from its saved
declaration. It preserves workdir/account binds but discards other container
files, so it is broader than clearing a tool cache. A controller-derived
`.cache` bind is kept by ordinary recreation: use `box cache clear BOX` while
stopped, or explicitly `box recreate --clear-cache BOX` to empty that separate
cache. Neither clears cache files that a tool keeps elsewhere in the layer. `space storage` help names
that consequence before suggesting it. [1](#source-1) [6](#source-6)

[FACT] Low-disk warnings and build-preflight refusals name both remedies:
stop then clear a separate cache (ordinary recreate keeps it), or explicitly
recreate a legacy layer (discarding non-bound files). Human `space storage`
output repeats that distinction in its footer, with in-box tool commands;
JSON remains the structured measurement alone. [7](#source-7) [8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [space_storage.go](../../cmd/yoloctl/space_storage.go#L400) — `newSpaceStorageCmd` help and explicit remedies.
2. <a id="source-2"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L2671) — `newAgentboxExecCmd` argv and confined command transport.
3. <a id="source-3"></a> [Go clean](https://pkg.go.dev/cmd/go#hdr-Remove_object_files_and_cached_files) — `-cache` and `-modcache`.
4. <a id="source-4"></a> [npm cache](https://docs.npmjs.com/cli/v11/commands/npm-cache/) — `clean` and required force flag.
5. <a id="source-5"></a> [pip cache](https://pip.pypa.io/en/stable/cli/pip_cache/) — `purge`.
6. <a id="source-6"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L2594) — recreate's kept binds and removal confirmation in `recreateAgentbox`.

7. <a id="source-7"></a> [storage_remedy.go](../../cmd/yoloctl/storage_remedy.go#L1) — shared explicit-action wording used by `diskReserveWarning`, `beforeBuild` and the storage report footer.
8. <a id="source-8"></a> [storage_warning.go](../../cmd/yoloctl/storage_warning.go#L30) — `diskReserveWarning` appends the shared explicit cache remedy.
9. <a id="source-9"></a> [storage_gc.go](../../cmd/yoloctl/storage_gc.go#L236) — `beforeBuild` includes the shared remedy in the measured reserve refusal.
