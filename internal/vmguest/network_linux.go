//go:build linux

package vmguest

import (
	"encoding/binary"
	"errors"
	"io"
	"os"

	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

func network() error {
	fd, e := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	tap := os.NewFile(uintptr(fd), "tap")
	req, e := unix.NewIfreq("eth0")
	if e != nil {
		_ = tap.Close()
		return e
	}
	req.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if e = unix.IoctlIfreq(fd, unix.TUNSETIFF, req); e != nil {
		_ = tap.Close()
		return e
	}
	conn, e := dialHost(vmwire.NetworkPort)
	if e != nil {
		_ = tap.Close()
		return e
	}
	for _, args := range [][]string{{"link", "set", "eth0", "address", "5a:94:ef:e4:0c:ee"}, {"addr", "add", "192.168.127.2/24", "dev", "eth0"}, {"link", "set", "eth0", "up"}, {"route", "add", "default", "via", "192.168.127.1"}} {
		if e = run("ip", args...); e != nil {
			_ = conn.Close()
			_ = tap.Close()
			return e
		}
	}
	go func() { defer func() { _ = conn.Close(); _ = tap.Close() }(); _ = sendFrames(conn, tap) }()
	go func() { defer func() { _ = conn.Close(); _ = tap.Close() }(); _ = receiveFrames(tap, conn) }()
	return nil
}
func sendFrames(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 65540)
	for {
		n, e := src.Read(buf[4:])
		if e != nil {
			return e
		}
		binary.BigEndian.PutUint32(buf[:4], uint32(n))
		if _, e = dst.Write(buf[:n+4]); e != nil {
			return e
		}
	}
}
func receiveFrames(dst io.Writer, src io.Reader) error {
	var header [4]byte
	buf := make([]byte, 65536)
	for {
		if _, e := io.ReadFull(src, header[:]); e != nil {
			return e
		}
		n := binary.BigEndian.Uint32(header[:])
		if n < 14 || n > uint32(len(buf)) {
			return errors.New("invalid Ethernet frame length")
		}
		if _, e := io.ReadFull(src, buf[:n]); e != nil {
			return e
		}
		if _, e := dst.Write(buf[:n]); e != nil {
			return e
		}
	}
}
