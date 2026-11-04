//go:build darwin

package resources

import (
	"context"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

func ReadHostMemory(ctx context.Context) (HostMemory, error) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return HostMemory{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/vm_stat")
	cmd.Env = []string{"LC_ALL=C"}
	raw, err := cmd.Output()
	if err != nil {
		return HostMemory{}, err
	}
	return parseVMStat(string(raw), total)
}
