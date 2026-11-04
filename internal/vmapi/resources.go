package vmapi

import (
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/resources"
)

// Resources are a VM's boot CPU count and memory size, such as "8GiB". They
// are concrete values: callers resolve "auto" before validating or applying.
type Resources struct {
	CPUs   int
	Memory string
}

// ErrNotStopped refuses a resource change while a VM is running, paused,
// starting or of uncertain state. Nothing was changed.
var ErrNotStopped = errors.New("stop the space before changing its VM resources")

// MaxCPUs is an adapter's boot vCPU maximum; zero means no fixed maximum.
func MaxCPUs(backend string) int {
	if backend == "firecracker" {
		return 32
	}
	return 0
}

// ValidateResources applies an adapter's boot bounds, so a caller can refuse
// values before saving them. An empty backend is a legacy Lima space.
func ValidateResources(backend string, r Resources) error {
	memory, err := resources.VMBytes(r.Memory)
	if err != nil {
		return err
	}
	switch backend {
	case "firecracker":
		if r.CPUs < 1 || r.CPUs > MaxCPUs(backend) {
			return errors.New("firecracker requires 1–32 CPUs")
		}
		if memory < 128<<20 || memory%(1<<20) != 0 {
			return errors.New("firecracker memory must be at least 128MiB in whole MiB")
		}
	case "lima", "":
		if r.CPUs < 1 {
			return errors.New("lima requires at least one CPU")
		}
	default:
		return fmt.Errorf("VM backend %q has no resource bounds", backend)
	}
	return nil
}
