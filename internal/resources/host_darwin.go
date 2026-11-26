//go:build darwin

package resources

import (
	"context"
	"os/exec"

	"golang.org/x/sys/unix"
)

func ReadHostMemory(ctx context.Context) (HostMemory, error) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return HostMemory{}, err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/vm_stat")
	cmd.Env = []string{"LC_ALL=C"}
	raw, err := cmd.Output()
	if err != nil {
		return HostMemory{}, err
	}
	return parseVMStat(string(raw), total)
}
