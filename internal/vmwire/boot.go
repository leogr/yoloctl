// Package vmwire contains the versioned microVM boot contract, without a host
// hypervisor or controller dependency.
package vmwire

import (
	"fmt"

	"github.com/leogr/yoloctl/internal/vmapi"
)

const Protocol = 4
const SSHPort = 5253
const NetworkPort = 5260
const SharePort = 5300
const GuestBinary = "/opt/yoloctl/bin/yoloctl"
const GuestControl = "/run/yoloctl-vm/control.sock"

type Boot struct {
	Schema   int           `json:"schema"`
	Name     string        `json:"name"`
	DiskID   string        `json:"disk_id"`
	UID      int           `json:"uid"`
	GID      int           `json:"gid"`
	Mounts   []vmapi.Mount `json:"mounts"`
	UnixNano int64         `json:"unix_nano"`
	// DataDiskGrow asks init to grow the data disk's filesystem before
	// mounting it: the host already extended the disk.
	DataDiskGrow *DataDiskGrow `json:"data_disk_grow,omitempty"`
}

// DataDiskGrow is a pending data disk grow from From to To bytes of the
// filesystem with exactly UUID.
type DataDiskGrow struct {
	UUID string `json:"uuid"`
	From uint64 `json:"from"`
	To   uint64 `json:"to"`
}

// GrownLine is what init prints to the console after a grow it verified
// with a clean e2fsck; the host looks for it before recording completion.
func (g DataDiskGrow) GrownLine() string {
	return fmt.Sprintf("yoloctl-init: data disk %s grown from %d to %d bytes; e2fsck clean", g.UUID, g.From, g.To)
}
