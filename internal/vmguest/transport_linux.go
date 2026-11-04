//go:build linux

package vmguest

import (
	"net"

	"github.com/mdlayher/vsock"
)

var dialHost = func(port uint32) (net.Conn, error) { return vsock.Dial(vsock.Host, port, nil) }
var prepareTransport = func() error { return nil }
var listenSSH = func(port uint32) (net.Listener, error) { return vsock.Listen(port, nil) }
var hostPeer = func(c net.Conn) bool {
	peer, ok := c.RemoteAddr().(*vsock.Addr)
	return ok && peer.ContextID == vsock.Host
}

var shareDebug bool
var testTransportPort = func(uint64) bool { return false }

var listenForward = func() (net.Listener, error) { return vsock.Listen(5254, nil) }
