package main

import (
	"context"

	"golang.org/x/sys/unix"
)

// installPrerequisite is the failed host prerequisite, or "" when it holds.
func installPrerequisite(context.Context) string {
	if err := unix.Access("/dev/kvm", unix.R_OK|unix.W_OK); err != nil {
		return "Firecracker needs readable and writable /dev/kvm (or select Lima)"
	}
	return ""
}
