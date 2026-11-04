//go:build !linux

package main

import (
	"errors"
	"net"
)

func verifyChatControlPeer(_ *net.UnixConn, _ string, _, _ int) error {
	return errors.New("managed chat control peer verification requires the native Linux controller")
}
