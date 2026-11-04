//go:build linux && microvmtest

package vmguest

// This file is excluded from production builds. QEMU TCG tests substitute only
// the vsock carrier; the guest supervisor, TAP framing, SFTP and SSH are shared.
import (
	"net"
	"strconv"
)

func init() {
	shareDebug = false
	testTransportPort = func(port uint64) bool { return port == 2223 || port == 2224 }
	listenForward = func() (net.Listener, error) { return net.Listen("tcp", "0.0.0.0:2224") }
	dialHost = func(port uint32) (net.Conn, error) { return net.Dial("tcp", "10.0.2.2:"+strconv.Itoa(int(port))) }
	listenSSH = func(uint32) (net.Listener, error) { return net.Listen("tcp", "0.0.0.0:2223") }
	hostPeer = func(net.Conn) bool { return true } // authentication remains SSH key based
	prepareTransport = func() error {
		for _, args := range [][]string{{"link", "set", "eth0", "name", "test0"}, {"addr", "add", "10.0.2.15/24", "dev", "test0"}, {"link", "set", "test0", "up"}} {
			if e := run("ip", args...); e != nil {
				return e
			}
		}
		return nil
	}
}
