package main

// storageCacheRemedy distinguishes the separate cache from legacy layer data.
// It describes explicit user actions; preflight never clears either itself.
const storageCacheRemedy = "separate cache: stop BOX, then yoloctl box cache clear BOX (ordinary recreate keeps it); caches in the layer: yoloctl box recreate BOX discards non-bound files and moves caches out; inside the box: go clean -cache -modcache, npm cache clean --force, pip cache purge, or rm -rf ~/.cargo/registry/cache"
