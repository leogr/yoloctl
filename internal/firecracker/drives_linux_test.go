//go:build linux && amd64

package firecracker

import "testing"

func TestPersistentDrivesHonorFlushAndDiscard(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		c := driveConfig("1", "disk.ext4", false, readOnly)
		if c["is_read_only"] != readOnly || c["io_engine"] != "Sync" {
			t.Fatalf("invalid drive: %v", c)
		}
		if !readOnly && (c["cache_type"] != "Writeback" || c["discard"] != true) {
			t.Fatalf("persistent disk lacks flush/discard: %v", c)
		}
		if readOnly && c["discard"] == true {
			t.Fatal("Firecracker rejects discard on immutable media")
		}
	}
}
