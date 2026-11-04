//go:build linux

package vmguest

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var ownedChildren sync.Map
var childrenMu sync.Mutex

func startOwned(cmd *exec.Cmd) error {
	childrenMu.Lock()
	defer childrenMu.Unlock()
	if e := cmd.Start(); e != nil {
		return e
	}
	ownedChildren.Store(cmd.Process.Pid, true)
	return nil
}

func reapOrphans() {
	for range time.NewTicker(time.Second).C {
		tasks, e := os.ReadDir("/proc/self/task")
		if e != nil {
			continue
		}
		childrenMu.Lock()
		for _, task := range tasks {
			raw, e := os.ReadFile("/proc/self/task/" + task.Name() + "/children")
			if e != nil {
				continue
			}
			for _, field := range strings.Fields(string(raw)) {
				pid, e := strconv.Atoi(field)
				if e != nil {
					continue
				}
				if _, owned := ownedChildren.Load(pid); owned {
					continue
				}
				var status unix.WaitStatus
				_, _ = unix.Wait4(pid, &status, unix.WNOHANG, nil)
			}
		}
		childrenMu.Unlock()
	}
}

func outputOwned(cmd *exec.Cmd) ([]byte, error) {
	var out bytes.Buffer
	cmd.Stdout = &out
	if e := startOwned(cmd); e != nil {
		return nil, e
	}
	e := cmd.Wait()
	ownedChildren.Delete(cmd.Process.Pid)
	return out.Bytes(), e
}
