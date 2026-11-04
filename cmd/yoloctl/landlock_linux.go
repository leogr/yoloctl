//go:build linux

package main

import "golang.org/x/sys/unix"

func queryLandlockABI() landlockKernelProbe {
	ret, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	return classifyLandlockABI(ret, errno)
}
