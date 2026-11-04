package main

import (
	"context"

	"golang.org/x/sys/unix"
)

func installPrerequisite(context.Context) string {
	if err := unix.Access("/dev/kvm", unix.R_OK|unix.W_OK); err != nil {
		return "Prerequisite unavailable: Firecracker needs readable/writable /dev/kvm (or select Lima)."
	}
	return "Prerequisite: /dev/kvm is accessible; live VM operation has not been tested."
}
