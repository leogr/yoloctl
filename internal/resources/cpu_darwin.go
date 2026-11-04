//go:build darwin

package resources

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

// ReadHostCPU reports macOS's currently available logical CPUs. It makes no
// cgroup-equivalent bandwidth claim and does not weigh P- and E-cores.
func ReadHostCPU(ctx context.Context) (HostCPU, error) {
	if err := ctx.Err(); err != nil {
		return HostCPU{}, err
	}
	n, err := unix.SysctlUint32("hw.logicalcpu")
	if err != nil {
		return HostCPU{}, err
	}
	if n == 0 || n > 1<<20 {
		return HostCPU{}, errors.New("invalid hw.logicalcpu")
	}
	return HostCPU{Logical: int(n)}, nil
}
