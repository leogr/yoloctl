//go:build linux && amd64

package firecracker

// Writable disks must honor guest fsync and return discarded blocks to the
// host. Firecracker v1.17 supports discard only with the synchronous IO engine.
func driveConfig(id, path string, root, readOnly bool) map[string]any {
	c := map[string]any{"drive_id": id, "path_on_host": path,
		"is_root_device": root, "is_read_only": readOnly, "io_engine": "Sync"}
	if !readOnly {
		c["cache_type"] = "Writeback"
		c["discard"] = true
	}
	return c
}
