//go:build !linux || !amd64

package main

import (
	"context"
	"errors"
)

func runVMInternal(context.Context, []string) error {
	return errors.New("the Firecracker backend requires a Linux amd64 host")
}
