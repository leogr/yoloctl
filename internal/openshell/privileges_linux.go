package openshell

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// execSupervisor is a one-shot child-process transition. It intentionally
// never unlocks its OS thread: after a partial transition the caller must exit,
// and after success exec replaces the process. No workload runs on this path.
// The boundary uses the separately qualified launch-capability-free entry.
func execSupervisor(binary string, args, environment []string) error {
	if os.Geteuid() != 0 {
		return errors.New("supervisor setup requires the privileged runtime child")
	}
	runtime.LockOSThread()
	last, err := clearBoundingCapabilities()
	if err != nil {
		return err
	}
	if err = unix.Prctl(unix.PR_SET_KEEPCAPS, 0, 0, 0, 0); err != nil {
		return err
	}
	if err = unix.Setgroups(nil); err != nil {
		return err
	}
	if err = unix.Setresgid(int(SupervisorGID), int(SupervisorGID), int(SupervisorGID)); err != nil {
		return err
	}
	if err = unix.Setresuid(int(SupervisorUID), int(SupervisorUID), int(SupervisorUID)); err != nil {
		return err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err = unix.Capset(&header, &data[0]); err != nil {
		return err
	}
	if err = unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return err
	}
	if err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err = verifySupervisorIdentity(last); err != nil {
		return err
	}
	return unix.Exec(binary, append([]string{binary}, args...), environment)
}

// Query the kernel instead of trusting a procfs cap_last_cap value. An unknown
// capability ABI fails closed; a build never silently keeps bounding bits.
func clearBoundingCapabilities() (int, error) {
	last := -1
	for capability := 0; capability <= 64; capability++ {
		_, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(capability), 0, 0, 0)
		if errors.Is(err, unix.EINVAL) {
			if last < 0 {
				return -1, errors.New("kernel does not support capability bounding verification")
			}
			return last, nil
		}
		if err != nil {
			return -1, err
		}
		if capability == 64 {
			return -1, errors.New("unsupported kernel capability ABI")
		}
		if err = unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0); err != nil {
			return -1, err
		}
		last = capability
	}
	return -1, errors.New("unbounded kernel capability ABI")
}

func verifySupervisorIdentity(lastCapability int) error {
	uid, euid, suid := unix.Getresuid()
	gid, egid, sgid := unix.Getresgid()
	if uid != int(SupervisorUID) || euid != uid || suid != uid || gid != int(SupervisorGID) || egid != gid || sgid != gid {
		return errors.New("supervisor retained the wrong real/effective/saved identity")
	}
	fsuid, err := unix.SetfsuidRetUid(-1)
	if err != nil || fsuid != uid {
		return errors.New("supervisor retained the wrong filesystem UID")
	}
	fsgid, err := unix.SetfsgidRetGid(-1)
	if err != nil || fsgid != gid {
		return errors.New("supervisor retained the wrong filesystem GID")
	}
	groups, err := unix.Getgroups()
	if err != nil || len(groups) != 0 {
		return errors.New("supervisor retained supplementary groups")
	}
	nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil || nnp != 1 {
		return errors.New("supervisor no-new-privileges is not set")
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return err
	}
	for _, part := range data {
		if part.Effective != 0 || part.Permitted != 0 || part.Inheritable != 0 {
			return errors.New("supervisor retained capabilities")
		}
	}
	if lastCapability < 0 || lastCapability > 63 {
		return errors.New("invalid capability qualification")
	}
	for capability := 0; capability <= lastCapability; capability++ {
		bounding, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(capability), 0, 0, 0)
		if err != nil {
			return err
		}
		ambient, err := unix.PrctlRetInt(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_IS_SET, uintptr(capability), 0, 0)
		if err != nil {
			return err
		}
		if bounding != 0 || ambient != 0 {
			return fmt.Errorf("supervisor retained bounding/ambient capability %d", capability)
		}
	}
	return nil
}
