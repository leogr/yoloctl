package openshell

import (
	"encoding/binary"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func runtimeLinkFixture(name string, flags uint32) syscall.NetlinkMessage {
	data := make([]byte, syscall.SizeofIfInfomsg)
	binary.NativeEndian.PutUint32(data[4:8], 1)
	binary.NativeEndian.PutUint32(data[8:12], flags)
	member := append([]byte(name), 0)
	size := 4 + len(member)
	attribute := make([]byte, (size+3)&^3)
	binary.NativeEndian.PutUint16(attribute[:2], uint16(size))
	binary.NativeEndian.PutUint16(attribute[2:4], syscall.IFLA_IFNAME)
	copy(attribute[4:], member)
	return syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: syscall.RTM_NEWLINK}, Data: append(data, attribute...)}
}

func TestConfinedInterfaceInventoryRequiresOnlyActiveLoopback(t *testing.T) {
	lo := runtimeLinkFixture("lo", unix.IFF_LOOPBACK|unix.IFF_UP|unix.IFF_RUNNING)
	done := syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: syscall.NLMSG_DONE}}
	if err := verifyRuntimeLoopbackOnly([]syscall.NetlinkMessage{lo, done}); err != nil {
		t.Fatal(err)
	}
	for _, messages := range [][]syscall.NetlinkMessage{
		nil, {done}, {lo, lo}, {lo, runtimeLinkFixture("eth0", unix.IFF_UP)},
		{runtimeLinkFixture("lo", unix.IFF_LOOPBACK)}, {runtimeLinkFixture("eth0", unix.IFF_LOOPBACK|unix.IFF_UP)},
		{runtimeLinkFixture("lo", unix.IFF_UP)}, {runtimeLinkFixture("lo\x00alias", unix.IFF_LOOPBACK|unix.IFF_UP)},
		{{Header: syscall.NlMsghdr{Type: syscall.RTM_NEWLINK}, Data: []byte{1}}},
	} {
		if err := verifyRuntimeLoopbackOnly(messages); err == nil {
			t.Fatal("incomplete or externally connected namespace accepted")
		}
	}
}
