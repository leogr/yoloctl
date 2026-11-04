package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

const activationMessageLimit = 16 << 10

// activationPacket uses Unix SOCK_SEQPACKET so the message and ancillary FD
// count have one unambiguous boundary. It always receives with CMSG_CLOEXEC;
// setting FD_CLOEXEC later would leave an inheritance race with another fork.
func activationPacket(conn *net.UnixConn, message any, expectedFDs int) ([]*os.File, error) {
	body := make([]byte, activationMessageLimit)
	control := make([]byte, unix.CmsgSpace(8*4))
	var n, cn, flags int
	var recvErr error
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	err = raw.Read(func(fd uintptr) bool {
		n, cn, flags, _, recvErr = unix.Recvmsg(int(fd), body, control, unix.MSG_CMSG_CLOEXEC)
		return !errors.Is(recvErr, unix.EAGAIN) && !errors.Is(recvErr, unix.EWOULDBLOCK)
	})
	if err != nil {
		return nil, err
	}
	if recvErr != nil {
		return nil, recvErr
	}
	var descriptors []*os.File
	valid := true
	messages, err := unix.ParseSocketControlMessage(control[:cn])
	if err != nil {
		valid = false
	}
	// Parse all delivered rights before any refusal so even extra/truncated
	// messages cannot leave a descriptor in this process.
	for _, item := range messages {
		if item.Header.Level != unix.SOL_SOCKET || item.Header.Type != unix.SCM_RIGHTS {
			valid = false
			continue
		}
		fds, err := unix.ParseUnixRights(&item)
		if err != nil {
			valid = false
			continue
		}
		for _, fd := range fds {
			descriptors = append(descriptors, os.NewFile(uintptr(fd), "openshell-activation-fd"))
		}
	}
	closeReceived := func() {
		for _, file := range descriptors {
			_ = file.Close()
		}
	}
	if !valid || flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || len(descriptors) != expectedFDs || n == 0 || n >= len(body) {
		closeReceived()
		return nil, errors.New("invalid, truncated or unexpected activation packet/descriptor count")
	}
	if err := checkJSONKeys(body[:n]); err != nil {
		closeReceived()
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body[:n]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(message); err != nil {
		closeReceived()
		return nil, errors.New("invalid activation message schema")
	}
	return descriptors, nil
}

func sendActivationPacket(conn *net.UnixConn, message any, descriptor *os.File) error {
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) >= activationMessageLimit {
		return errors.New("activation message exceeds its bound")
	}
	var control []byte
	if descriptor != nil {
		control = unix.UnixRights(int(descriptor.Fd()))
	}
	n, cn, err := conn.WriteMsgUnix(raw, control, nil)
	if err != nil {
		return err
	}
	if n != len(raw) || cn != len(control) {
		return errors.New("incomplete activation packet send")
	}
	return nil
}
