//go:build linux

package main

import (
	"os"
	"syscall"
)

// signalOnce sends one signal to a test child as a thread-directed tgkill to
// its main thread. Inside a box the sandbox broker delivers a process kill
// itself and a restart of that call delivers it again, so one kill can reach
// the child twice; the broker continues tgkill to the kernel, which performs
// it once. The child's handler receives it as a process signal either way.
func signalOnce(p *os.Process, sig syscall.Signal) error {
	return syscall.Tgkill(p.Pid, p.Pid, sig)
}
