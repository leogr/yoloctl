# QCOW2 blank-disk fixture

`blank.qcow2.gz` is a synthetic standalone QCOW2 v3 disk, compressed only to
avoid checking in 256 KiB of mostly zeros. It has a 2 MiB guest-visible size,
64 KiB clusters, no backing file, encryption, snapshots or incompatible features.
Its four allocated metadata clusters are header, refcount table, refcount block
and empty L1 table. The refcounts cover those four clusters; all guest clusters
are unallocated and read as zero.

The layout follows the [QEMU QCOW2 specification](https://www.qemu.org/docs/master/interop/qcow2.html).
This exercises real format decoding without requiring QEMU in every test
installation. An additional test creates fresh images with `qemu-img` and writes
nonzero sectors with `qemu-io` when those tools are available.
