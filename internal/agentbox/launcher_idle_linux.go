package agentbox

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// OtherContainerProcesses counts the processes in the PID namespace of the
// container whose PID 1 has VM PID pid1, other than that PID 1, from one read
// of the VM's /proc. Any entry that cannot be read is an error (a complete
// observation or none): the caller treats an error as "not idle".
func OtherContainerProcesses(pid1 int) (int, error) {
	if pid1 <= 1 {
		return 0, errors.New("the container's PID 1 is unknown")
	}
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid1))
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	others := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == pid1 {
			continue
		}
		ns, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid))
		if errors.Is(err, os.ErrNotExist) {
			continue // ended during the read
		}
		if err != nil {
			return 0, err
		}
		if ns == target {
			others++
		}
	}
	return others, nil
}
