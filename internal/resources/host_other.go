//go:build !linux && !darwin

package resources

import (
	"context"
	"errors"
)

func ReadHostMemory(context.Context) (HostMemory, error) {
	return HostMemory{}, errors.New("automatic VM memory requires a Linux or macOS host; pass --memory explicitly")
}
