//go:build !linux && !darwin

package resources

import (
	"context"
	"errors"
)

func ReadHostCPU(context.Context) (HostCPU, error) {
	return HostCPU{}, errors.New("automatic VM CPUs require a Linux or macOS host; pass --cpus explicitly")
}
