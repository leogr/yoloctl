//go:build linux

package resources

import (
	"context"
	"os"
)

func ReadHostMemory(ctx context.Context) (HostMemory, error) {
	if err := ctx.Err(); err != nil {
		return HostMemory{}, err
	}
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return HostMemory{}, err
	}
	return parseMemInfo(string(raw))
}
