// Package vmapi defines the host VM boundary independently of a hypervisor.
package vmapi

type Mount struct {
	// Location is the canonical host path shared into the VM.
	// The default guest destination is the same absolute path.
	Location string
	// MountPoint overrides the guest destination for read-only infrastructure
	// catalogs. Ordinary project grants retain their original host paths.
	MountPoint string
	// Uncached gives request snapshots fresh host catalog replacements.
	// Lima ignores the 9p transport options when another mount type is selected.
	Uncached bool
	// Writable=true grants the VM (and through it, agentbox containers)
	// write access. Default false (read-only).
	Writable bool
}

type Instance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// StatusDetail explains a live VM paused for host storage recovery.
	StatusDetail string `json:"status_detail,omitempty"`
	VMType       string `json:"vmType"`
	Arch         string `json:"arch"`
	CPUs         int    `json:"cpus"`
	Memory       uint64 `json:"memory"`
	Disk         uint64 `json:"disk"`
	Dir          string `json:"dir"`
}

type DiskInfo struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
	Format   string `json:"format"`
}

// Spec describes boot resources and the exact host grants. Adapters cannot
// derive broader grants from guest requests or box declarations.
type Spec struct {
	Mounts []Mount `json:"mounts"`
	DiskID string  `json:"disk_id"`
	Format bool    `json:"format"`
	CPUs   int     `json:"cpus"`
	Memory string  `json:"memory"`
	Disk   string  `json:"disk"`
	Image  string  `json:"image,omitempty"`
}
