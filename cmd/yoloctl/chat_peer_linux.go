//go:build linux

package main

import (
	"encoding/hex"
	"errors"
	"net"

	"github.com/leogr/yoloctl/internal/spacetelemetry"
	"golang.org/x/sys/unix"
)

// The live socket peer must be a process of this exact container, under the
// sandbox identity. A leftover socket from a different server incarnation is
// never evidence that the current managed workload applied its configuration.
func verifyChatControlPeer(conn *net.UnixConn, containerID string, uid, gid int) error {
	id, err := hex.DecodeString(containerID)
	if err != nil || len(id) != 32 {
		return errors.New("managed chat requires a full container identity")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) { cred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return err
	}
	if sockErr != nil {
		return sockErr
	}
	if cred == nil || cred.Pid <= 0 || cred.Uid != uint32(uid) || cred.Gid != uint32(gid) {
		return errors.New("managed chat control peer has the wrong native identity")
	}
	_, err = chatPeerCollector(currentServiceManager(), "/proc", "/sys/fs/cgroup").BoxCgroup(spacetelemetry.Target{PID: int(cred.Pid), ID: containerID})
	return err
}

// chatPeerCollector reads a chat peer's cgroup within the box workload pool
// of the guest's service manager, as space telemetry does: the microvm group
// on Firecracker, the systemd slice on Lima.
func chatPeerCollector(serviceManager, procRoot, cgroupRoot string) spacetelemetry.Collector {
	return spacetelemetry.Collector{ProcRoot: procRoot, CgroupRoot: cgroupRoot, PoolRel: workloadPoolRel(serviceManager)}
}
