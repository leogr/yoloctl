//go:build linux

package vmguest

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/rotatelog"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

type managedUnit struct {
	mu       sync.Mutex
	spec     vmwire.Unit
	cmd      *exec.Cmd
	logDone  <-chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopping bool
	failed   error
}

var unitManager = struct {
	sync.Mutex
	units map[string]*managedUnit
}{units: map[string]*managedUnit{}}

func validateUnit(u vmwire.Unit, start bool) error {
	if u.Name == vmwire.AuditUnitName {
		expected := vmwire.AuditUnit()
		if start && (u.Restart || !slices.Equal(u.Args, expected.Args) || !slices.Equal(u.Env, expected.Env)) {
			return errors.New("audit must use its fixed launcher and environment without restart")
		}
		return nil
	}
	if len(u.Name) > 180 || !strings.HasSuffix(u.Name, ".service") || strings.ContainsAny(u.Name, "/\\\x00\r\n") || (!strings.HasPrefix(u.Name, "yoloctl-session-") && !strings.HasPrefix(u.Name, "yoloctl-port-") && !strings.HasPrefix(u.Name, port.OpenShellUnitPrefix)) {
		return errors.New("invalid managed service identity")
	}
	if !start {
		return nil
	}
	if len(u.Args) == 0 || len(u.Args) > 512 {
		return errors.New("invalid managed service argv")
	}
	if strings.HasPrefix(u.Name, "yoloctl-session-") {
		if u.Restart || len(u.Args) < 3 || u.Args[0] != vmwire.GuestBinary || u.Args[1] != "session-hold" {
			return errors.New("holder must use the fixed launcher without restart")
		}
	} else if strings.HasPrefix(u.Name, port.OpenShellUnitPrefix) {
		if !u.Restart || len(u.Args) < 3 || u.Args[0] != vmwire.GuestBinary || u.Args[1] != port.ForwardWorkerCommand {
			return errors.New("invalid OpenShell port service launcher")
		}
		spec, binding, err := port.ParseForwardWorker(u.Args[2:])
		name, nameErr := port.OpenShellUnitName(spec)
		args, argsErr := port.ForwardWorkerArgs(spec, binding)
		if err != nil || nameErr != nil || argsErr != nil || name != u.Name || !slices.Equal(u.Args[1:], args) || !slices.Equal(u.Env, []string{"HOME=/root", "YOLOCTL_CONTROLLER_CONTEXT=/etc/yoloctl/controller.json"}) {
			return errors.New("OpenShell port service descriptor/environment mismatch")
		}
	} else if !u.Restart || len(u.Args) != 3 || u.Args[0] != "/usr/bin/socat" || !strings.HasPrefix(u.Args[1], "TCP-LISTEN:") || !strings.HasPrefix(u.Args[2], "TCP:") {
		return errors.New("invalid port service launcher")
	}
	for _, env := range u.Env {
		if env != "HOME=/root" && env != "YOLOCTL_CONTROLLER_CONTEXT=/etc/yoloctl/controller.json" {
			return errors.New("unsupported service environment")
		}
	}
	return nil
}
func unitGroup(name string) string { return filepath.Join("/sys/fs/cgroup/yoloctl-units", name) }

// Use a fresh, empty cgroup for every incarnation. Older kernels can SIGKILL
// CLONE_INTO_CGROUP children after cgroup.kill was written even once (upstream
// fix 8e359920216689b3b79e0fe8961a77fe312a511f). Never reuse that kill sequence.
func prepareGroup(path string) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	if e := os.Mkdir(path, 0755); e == nil {
		return nil
	} else if !errors.Is(e, os.ErrExist) {
		return e
	}
	if e := cleanGroup(path); e != nil {
		return e
	}
	if e := removeGroup(path); e != nil {
		return e
	}
	return os.Mkdir(path, 0755)
}
func removeGroup(path string) error {
	entries, e := os.ReadDir(path)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if e = removeGroup(filepath.Join(path, entry.Name())); e != nil {
				return e
			}
		}
	}
	return os.Remove(path)
}
func cleanGroup(path string) error {
	if e := os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0600); e != nil {
		return e
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		raw, e := os.ReadFile(filepath.Join(path, "cgroup.events"))
		if e != nil {
			return e
		}
		if strings.Contains(string(raw), "populated 0") {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("guest service cgroup did not terminate")
		}
		time.Sleep(time.Millisecond)
	}
}
func (u *managedUnit) start() error {
	group := unitGroup(u.spec.Name)
	if e := prepareGroup(group); e != nil {
		return e
	}
	fd, e := unix.Open(group, unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	defer func() { _ = unix.Close(fd) }()
	logDir := "/var/log/yoloctl-units"
	if e = os.MkdirAll(logDir, 0700); e != nil {
		return e
	}
	log, e := rotatelog.Open(filepath.Join(logDir, u.spec.Name+".log"), 1<<20)
	if e != nil {
		return e
	}
	output, drained, e := rotatelog.Pipe(log.BestEffort(), func() {
		if err := log.Err(); err != nil {
			slog.Warn("Service log write failed", "actor", "vm", "unit", u.spec.Name, "err", err)
		}
		_ = log.Close()
	})
	if e != nil {
		_ = log.Close()
		return e
	}
	// The supervisor closes only its own write descriptor after Start. The
	// relay owns the sink until the final inherited writer closes it.
	defer func() { _ = output.Close() }()
	cmd := exec.Command(u.spec.Args[0], u.spec.Args[1:]...)
	cmd.Env = append(os.Environ(), u.spec.Env...)
	if strings.HasPrefix(u.spec.Name, port.OpenShellUnitPrefix) || u.spec.Name == vmwire.AuditUnitName {
		cmd.Env = append([]string{"PATH=" + vmwire.GuestSystemPath}, u.spec.Env...)
	}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, UseCgroupFD: true, CgroupFD: fd}
	if e = startOwned(cmd); e != nil {
		return e
	}
	u.cmd, u.logDone = cmd, drained
	return nil
}
func (u *managedUnit) watch() {
	defer close(u.done)
	for {
		u.mu.Lock()
		cmd, drained := u.cmd, u.logDone
		u.mu.Unlock()
		e := cmd.Wait()
		ownedChildren.Delete(cmd.Process.Pid)
		cleanup := cleanGroup(unitGroup(u.spec.Name))
		// A successful cgroup cleanup has retired the inherited writers: join
		// the consumer before publishing final state. If cleanup failed, retain
		// the relay instead of closing its reader and losing descendant output.
		if cleanup == nil && drained != nil {
			<-drained
		}
		u.mu.Lock()
		u.cmd = nil
		u.failed = errors.Join(e, cleanup)
		restart := !u.stopping && u.spec.Restart && e != nil && cleanup == nil
		u.mu.Unlock()
		if !restart {
			return
		}
		select {
		case <-u.stop:
			return
		case <-time.After(500 * time.Millisecond):
		}
		u.mu.Lock()
		if u.stopping {
			u.mu.Unlock()
			return
		}
		e = u.start()
		u.failed = e
		u.mu.Unlock()
		if e != nil {
			return
		}
	}
}
func manageUnit(r vmwire.UnitRequest) (string, error) {
	if r.Action == vmwire.AuditCapabilityAction {
		if r.Unit.Name != vmwire.AuditUnitName || len(r.Unit.Args) != 0 || len(r.Unit.Env) != 0 || r.Unit.Restart {
			return "", errors.New("invalid audit capability query")
		}
		return vmwire.AuditCapability, nil
	}
	if r.Action == "list" || r.Action == "inventory" {
		if r.Unit.Name != vmwire.LegacyPortUnitPrefix && r.Unit.Name != vmwire.OpenShellPortUnitPrefix && r.Unit.Name != "yoloctl-session-" {
			return "", errors.New("unsupported service inventory")
		}
		unitManager.Lock()
		defer unitManager.Unlock()
		var names []string
		for name, u := range unitManager.units {
			u.mu.Lock()
			matches := strings.HasPrefix(name, r.Unit.Name) || (r.Unit.Name == vmwire.LegacyPortUnitPrefix && strings.HasPrefix(name, vmwire.OpenShellPortUnitPrefix))
			// The logical port view includes retained/restarting services of
			// both transports. Keep the old list opcode for legacy VM images;
			// new native effects separately require the inventory opcode.
			if matches && (r.Action == "inventory" || r.Unit.Name == vmwire.LegacyPortUnitPrefix || (u.cmd != nil && !u.stopping)) {
				names = append(names, name)
			}
			u.mu.Unlock()
		}
		sort.Strings(names)
		return strings.Join(names, "\n"), nil
	}
	if e := validateUnit(r.Unit, r.Action == "start"); e != nil {
		return "", e
	}
	unitManager.Lock()
	u := unitManager.units[r.Unit.Name]
	if r.Action == "start" {
		if u != nil {
			select {
			case <-u.done:
			default:
				unitManager.Unlock()
				return "", errors.New("managed service already exists")
			}
		}
		u = &managedUnit{spec: r.Unit, stop: make(chan struct{}), done: make(chan struct{})}
		if e := u.start(); e != nil {
			unitManager.Unlock()
			return "", e
		}
		unitManager.units[r.Unit.Name] = u
		unitManager.Unlock()
		go u.watch()
		return "", nil
	}
	unitManager.Unlock()
	switch r.Action {
	case "process":
		observation := vmwire.UnitProcess{Name: r.Unit.Name, State: "inactive"}
		if u != nil {
			u.mu.Lock()
			if u.cmd != nil && !u.stopping {
				observation.State, observation.PID = "active", u.cmd.Process.Pid
			} else if u.stopping {
				observation.State = "deactivating"
				if u.cmd != nil {
					observation.PID = u.cmd.Process.Pid
				}
			} else {
				select {
				case <-u.done:
					if u.failed != nil {
						observation.State = "failed"
					}
				default:
					observation.State = "activating"
				}
			}
			u.mu.Unlock()
		}
		raw, err := json.Marshal(observation)
		return string(raw), err
	case "state":
		if u == nil {
			return "inactive", nil
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.cmd != nil && !u.stopping {
			return "active", nil
		}
		select {
		case <-u.done:
			if u.failed != nil {
				return "failed", nil
			}
			return "inactive", nil
		default:
			return "activating", nil
		}
	case "stop":
		if u == nil {
			return "", nil
		}
		u.mu.Lock()
		if !u.stopping {
			u.stopping = true
			close(u.stop)
			if u.cmd != nil {
				_ = u.cmd.Process.Signal(syscall.SIGTERM)
			}
		}
		u.mu.Unlock()
		// A finished service can be replaced under the same name. An old stop
		// request must never kill the replacement's cgroup.
		cleanup := func(remove bool) error {
			unitManager.Lock()
			defer unitManager.Unlock()
			if unitManager.units[r.Unit.Name] != u {
				return nil
			}
			if e := cleanGroup(unitGroup(u.spec.Name)); e != nil {
				return e
			}
			if remove {
				delete(unitManager.units, r.Unit.Name)
			}
			return nil
		}
		select {
		case <-u.done:
		case <-time.After(10 * time.Second):
			if e := cleanup(false); e != nil {
				return "", e
			}
			select {
			case <-u.done:
			case <-time.After(3 * time.Second):
				return "", errors.New("guest service did not stop")
			}
		}
		return "", cleanup(true)
	default:
		return "", fmt.Errorf("unknown guest service action %q", r.Action)
	}
}
