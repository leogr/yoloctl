# The VM disk is full

Use ordinary `yoloctl space storage SPACE` to identify usage. A source builder
hands live storage work to the assigned DevOps/operator; a copied skill does
not grant permission to change a box or space.

For a box with the controller-derived `/home/sandbox/.cache` bind, stop it and
use `yoloctl box cache clear NAME --space SPACE`. Running or unknown runtime
state refuses the clear. Ordinary recreate keeps this separate cache; only
explicit `box recreate --clear-cache NAME` clears it during recreation.

For a legacy box whose caches live in its writable layer, explicit recreate
moves new caches to the separate directory and loses files outside its binds.
Do not present ordinary recreate as a way to reclaim an already separate cache.
Tool-specific alternatives inside an authorized box include `go clean -cache
-modcache`, `npm cache clean --force`, and `pip cache purge`. A tool configured
outside `.cache` writes elsewhere; this bind is not a quota or enforcement.

Cache clearing never touches account credentials, the workdir or another box.
Automatic cleanup still owns only unused composed images and BuildKit cache.
Never widen it to generic container/volume pruning or unrequested user files.
See the repository [cache contract](../../../docs/digests/box-caches.md).

Cargo cache downloads can be cleared explicitly inside the owning box with
`rm -rf ~/.cargo/registry/cache`; do not remove `.cargo/bin`, configuration or
credentials. This is a user-requested cache action, never automatic cleanup.
