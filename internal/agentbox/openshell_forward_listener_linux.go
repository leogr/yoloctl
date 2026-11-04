package agentbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type forwardListenerIdentity struct {
	FD    int
	Inode uint64
}

// withForwardListener observes only the service's actual bound TCP listener.
// Control retains its FD while status is published; no caller path or port
// probe can substitute a different process's listener.
func withForwardListener(listener net.Listener, hostPort int, work func(forwardListenerIdentity) error) error {
	connection, ok := listener.(syscall.Conn)
	if !ok || work == nil {
		return errors.New("forward listener lacks a kernel socket descriptor")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return err
	}
	var result error
	err = raw.Control(func(fd uintptr) {
		var stat unix.Stat_t
		if fd < 3 || fd > 1<<20 || unix.Fstat(int(fd), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Ino == 0 {
			result = errors.New("forward listener is not a current socket descriptor")
			return
		}
		address, addressErr := unix.Getsockname(int(fd))
		tcp, tcpOK := address.(*unix.SockaddrInet4)
		accepting, acceptErr := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
		if addressErr != nil || !tcpOK || tcp.Port != hostPort || tcp.Addr != [4]byte{127, 0, 0, 1} || acceptErr != nil || accepting != 1 {
			result = errors.New("forward descriptor is not the declared VM-loopback TCP listener")
			return
		}
		result = work(forwardListenerIdentity{int(fd), stat.Ino})
	})
	return errors.Join(err, result)
}

// verifyListener complements process/status proof: a live process can be
// exiting after it already closed its listener. Read only that pinned process's
// kernel FD and TCP tables; never connect to an arbitrary competing listener.
func (p *pinnedForwardProcess) verifyListener(ctx context.Context, hostPort int, socket forwardListenerIdentity) error {
	if hostPort < 1 || hostPort > 65535 || socket.FD < 3 || socket.FD > 1<<20 || socket.Inode == 0 {
		return errors.New("invalid forward listener identity")
	}
	if err := p.verify(ctx); err != nil {
		return err
	}
	descriptors, err := unix.Openat(p.process, "fd", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(descriptors) }()
	verifyDescriptor := func() error {
		var target [128]byte
		n, err := unix.Readlinkat(descriptors, strconv.Itoa(socket.FD), target[:])
		if err != nil {
			return err
		}
		if n == len(target) || string(target[:n]) != fmt.Sprintf("socket:[%d]", socket.Inode) {
			return errors.New("forward worker no longer holds its published socket")
		}
		return nil
	}
	if err := verifyDescriptor(); err != nil {
		return err
	}
	network, err := unix.Openat(p.process, "net", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(network) }()
	fd, err := unix.Openat(network, "tcp", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "forward-process-kernel-tcp")
	raw, readErr := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if len(raw) > 2<<20 {
		return errors.New("forward process TCP table exceeds bound")
	}
	if err := verifyForwardTCPTable(raw, hostPort, socket.Inode, p.uid); err != nil {
		return err
	}
	if err := verifyDescriptor(); err != nil {
		return err
	}
	return p.verify(ctx)
}

func verifyForwardTCPTable(raw []byte, hostPort int, inode uint64, uid uint32) error {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	if !scanner.Scan() {
		return errors.New("missing forward process TCP table")
	}
	header := strings.Fields(scanner.Text())
	if len(header) < 4 || header[0] != "sl" || header[1] != "local_address" || header[2] != "rem_address" || header[3] != "st" {
		return errors.New("unexpected forward process TCP table schema")
	}
	wantInode := strconv.FormatUint(inode, 10)
	found := false
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			return errors.New("incomplete forward process TCP table row")
		}
		if fields[9] != wantInode {
			continue
		}
		if found || fields[1] != fmt.Sprintf("0100007F:%04X", hostPort) || fields[2] != "00000000:0000" || fields[3] != "0A" || fields[7] != strconv.FormatUint(uint64(uid), 10) {
			return errors.New("published forward socket is not the declared owned listening endpoint")
		}
		found = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !found {
		return errors.New("published forward socket is no longer listening")
	}
	return nil
}
