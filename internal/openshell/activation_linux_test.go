package openshell

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func activationTestPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var conns []*net.UnixConn
	for _, fd := range fds {
		file := os.NewFile(uintptr(fd), "activation-test")
		conn, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		unixConn := conn.(*net.UnixConn)
		if err := unixConn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, unixConn)
		t.Cleanup(func() { _ = unixConn.Close() })
	}
	return conns[0], conns[1]
}

func TestActivationDescriptorsArriveCloseOnExecAndHaveExactCount(t *testing.T) {
	left, right := activationTestPair(t)
	file, err := os.CreateTemp(t.TempDir(), "descriptor")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	message := activationEnvelope{Message: activationMessage{Kind: "offer"}}
	if err := sendActivationPacket(left, message, file); err != nil {
		t.Fatal(err)
	}
	var received activationEnvelope
	fds, err := activationPacket(right, &received, 1)
	if err != nil || len(fds) != 1 || received != message {
		t.Fatal(received, err)
	}
	flags, err := unix.FcntlInt(fds[0].Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("received FD has an exec inheritance window", flags, err)
	}
	source, _ := file.Stat()
	copy, _ := fds[0].Stat()
	if !os.SameFile(source, copy) {
		t.Fatal("received a different object")
	}
	_ = fds[0].Close()
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("fd leak accounting requires Linux procfs")
	}
	for _, count := range []int{0, 2, 12} {
		rights := make([]int, count)
		for n := range rights {
			rights[n] = int(file.Fd())
		}
		var control []byte
		if count > 0 {
			control = unix.UnixRights(rights...)
		}
		if _, _, err := left.WriteMsgUnix([]byte(`{"message":{"kind":"offer"},"mac":""}`), control, nil); err != nil {
			t.Fatal(err)
		}
		if fds, err := activationPacket(right, &received, 1); err == nil || len(fds) != 0 {
			t.Fatal("unexpected/truncated descriptor count accepted")
		}
	}
	for _, raw := range []string{`{"message":{"kind":"offer","kind":"other"}}`, `{"unknown":1}`, strings.Repeat("x", activationMessageLimit+1)} {
		if _, _, err := left.WriteMsgUnix([]byte(raw), unix.UnixRights(int(file.Fd())), nil); err != nil {
			t.Fatal(err)
		}
		if fds, err := activationPacket(right, &received, 1); err == nil || len(fds) != 0 {
			t.Fatal("malformed activation packet accepted")
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("activation refusal leaked descriptors: %d -> %d", len(before), len(after))
	}
}

func TestActivationProcessMetadataRejectsStaleOrDeadIdentities(t *testing.T) {
	fields := make([]string, 20)
	for n := range fields {
		fields[n] = "0"
	}
	fields[0] = "S"
	fields[19] = "12345"
	stat := "712 (name ) with ) parens) " + strings.Join(fields, " ")
	if start, err := processStartIdentity([]byte(stat), 712); err != nil || start != "12345" {
		t.Fatal(start, err)
	}
	for _, bad := range []string{strings.Replace(stat, "712", "713", 1), strings.Replace(stat, ") S ", ") Z ", 1), strings.Replace(stat, "12345", "0", 1), "712 (short) S 0 0", strings.Replace(stat, "12345", "unknown", 1)} {
		if _, err := processStartIdentity([]byte(bad), 712); err == nil {
			t.Fatal("invalid process identity accepted", bad)
		}
	}
	if err := nativePreparedIdentity(PreparedRuntime{}); err == nil {
		t.Fatal("host caller gained native activation authority")
	}
}

func TestActivationAuthenticationRejectsReplayAndPeerPhaseChanges(t *testing.T) {
	var key activationKey
	for n := range key {
		key[n] = byte(n + 1)
	}
	message := activationMessage{Kind: "authorize", Schema: BootstrapSchema, RecordSHA256: strings.Repeat("a", 64), ClientNonce: strings.Repeat("b", 64), ServerNonce: strings.Repeat("c", 64), Namespace: namespaceIdentity{123, 456}, ProofSHA256: strings.Repeat("d", 64)}
	envelope := key.authenticate(message)
	if err := key.verify(envelope, message); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*activationMessage){
		func(m *activationMessage) { m.Kind = "offer" }, func(m *activationMessage) { m.Schema-- },
		func(m *activationMessage) { m.RecordSHA256 = strings.Repeat("f", 64) }, func(m *activationMessage) { m.ClientNonce = strings.Repeat("f", 64) },
		func(m *activationMessage) { m.ServerNonce = strings.Repeat("f", 64) }, func(m *activationMessage) { m.Namespace.Inode++ }, func(m *activationMessage) { m.ProofSHA256 = strings.Repeat("f", 64) },
	} {
		changed := message
		mutate(&changed)
		if key.verify(envelope, changed) == nil {
			t.Fatal("activation could be replayed into another binding or phase")
		}
	}
	other := key
	other[0] ^= 255
	if other.verify(envelope, message) == nil {
		t.Fatal("unprovisioned native peer authenticated")
	}
	if strings.Contains(fmt.Sprintf("%v %#v", key, key), "[1 2 3") {
		t.Fatal("activation key formatting discloses secret material")
	}
}

func TestActivationReceiveHasABoundedDeadline(t *testing.T) {
	_, right := activationTestPair(t)
	if err := right.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var message activationEnvelope
	if _, err := activationPacket(right, &message, 0); err == nil {
		t.Fatal("unresponsive activation peer was accepted")
	}
}
