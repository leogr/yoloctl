//go:build !linux

package main

import (
	"os"
	"syscall"
)

// signalOnce sends one signal to a test child (see the Linux variant).
func signalOnce(p *os.Process, sig syscall.Signal) error {
	return p.Signal(sig)
}
