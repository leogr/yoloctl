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

type unitName struct {
	held     chan struct{}
	users    int
	starting bool
}

type unitRegistry struct {
	sync.Mutex
	units  map[string]*managedUnit
	names  map[string]*unitName
	launch func(*managedUnit) error
	clean  func(string) error
}

var unitManager = unitRegistry{
	units:  make(map[string]*managedUnit),
	names:  make(map[string]*unitName),
	launch: (*managedUnit).start,
	clean:  cleanGroup,
}

// A reference covers callers waiting for the guard as well as its owner.
func (m *unitRegistry) lockName(name string) (*unitName, func()) {
	m.Lock()
	entry := m.names[name]
	if entry == nil {
		entry = &unitName{held: make(chan struct{}, 1)}
		m.names[name] = entry
	}
	entry.users++
	m.Unlock()
	entry.held <- struct{}{}
	return entry, func() {
		m.Lock()
		<-entry.held
		entry.users--
		if entry.users == 0 {
			delete(m.names, name)
		}
		m.Unlock()
	}
}

func (m *unitRegistry) lookup(name string) *managedUnit {
	m.Lock()
	defer m.Unlock()
	return m.units[name]
}

func (m *unitRegistry) observe(name string) *managedUnit {
	m.Lock()
	entry, unit := m.names[name], m.units[name]
	pending := entry != nil && entry.starting
	m.Unlock()
	if !pending {
		return unit
	}
	_, unlock := m.lockName(name)
	defer unlock()
	return m.lookup(name)
}

func validateUnit(u vmwire.Unit, start bool) error {
	if u.Name == vmwire.AuditUnitName {
		expected := vmwire.AuditUnit()
		if start && (u.Restart || !slices.Equal(u.Args, expected.Args) || !slices.Equal(u.Env, expected.Env)) {
			return errors.New("audit must use its fixed launcher and environment without restart")
		}
		return nil
	}
	if !vmwire.ManagedUnitName(u.Name) {
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
func (m *unitRegistry) watch(u *managedUnit) {
	defer close(u.done)
	for {
		u.mu.Lock()
		cmd, drained := u.cmd, u.logDone
		u.mu.Unlock()
		e := cmd.Wait()
		ownedChildren.Delete(cmd.Process.Pid)
		cleanup := m.clean(unitGroup(u.spec.Name))
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
		e = m.launch(u)
		u.failed = e
		u.mu.Unlock()
		if e != nil {
			return
		}
	}
}
func manageUnit(r vmwire.UnitRequest) (string, error) {
	return unitManager.manage(r)
}

func (m *unitRegistry) list(action, prefix string) string {
	complete := action == "inventory" || prefix == vmwire.LegacyPortUnitPrefix
	matches := func(name string) bool {
		return strings.HasPrefix(name, prefix) ||
			prefix == vmwire.LegacyPortUnitPrefix && strings.HasPrefix(name, vmwire.OpenShellPortUnitPrefix)
	}
	m.Lock()
	var names []string
	for name := range m.units {
		if matches(name) {
			names = append(names, name)
		}
	}
	for name, entry := range m.names {
		if entry.starting && m.units[name] == nil && matches(name) {
			names = append(names, name)
		}
	}
	m.Unlock()
	if !complete {
		names = slices.DeleteFunc(names, func(name string) bool {
			u := m.observe(name)
			if u == nil {
				return true
			}
			u.mu.Lock()
			defer u.mu.Unlock()
			return u.cmd == nil || u.stopping
		})
	}
	sort.Strings(names)
	return strings.Join(names, "\n")
}

func (m *unitRegistry) start(spec vmwire.Unit) error {
	entry, unlock := m.lockName(spec.Name)
	defer unlock()
	if previous := m.lookup(spec.Name); previous != nil {
		select {
		case <-previous.done:
		default:
			return errors.New("managed service already exists")
		}
	}
	u := &managedUnit{spec: spec, stop: make(chan struct{}), done: make(chan struct{})}
	m.Lock()
	entry.starting = true
	m.Unlock()
	err := m.launch(u)
	m.Lock()
	entry.starting = false
	if err == nil {
		m.units[spec.Name] = u
	}
	m.Unlock()
	if err == nil {
		go m.watch(u)
	}
	return err
}

func (m *unitRegistry) cleanup(u *managedUnit, remove bool) error {
	name := u.spec.Name
	_, unlock := m.lockName(name)
	defer unlock()
	// A finished service can be replaced under the same name.
	if m.lookup(name) != u {
		return nil
	}
	if err := m.clean(unitGroup(name)); err != nil {
		return err
	}
	if remove {
		m.Lock()
		delete(m.units, name)
		m.Unlock()
	}
	return nil
}

func (m *unitRegistry) stop(name string) error {
	_, unlock := m.lockName(name)
	u := m.lookup(name)
	if u == nil {
		unlock()
		return nil
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
	unlock()
	select {
	case <-u.done:
	case <-time.After(10 * time.Second):
		if err := m.cleanup(u, false); err != nil {
			return err
		}
		select {
		case <-u.done:
		case <-time.After(3 * time.Second):
			return errors.New("guest service did not stop")
		}
	}
	return m.cleanup(u, true)
}

func (m *unitRegistry) manage(r vmwire.UnitRequest) (string, error) {
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
		return m.list(r.Action, r.Unit.Name), nil
	}
	if e := validateUnit(r.Unit, r.Action == "start"); e != nil {
		return "", e
	}
	switch r.Action {
	case "start":
		return "", m.start(r.Unit)
	case "stop":
		return "", m.stop(r.Unit.Name)
	case "process":
		u := m.observe(r.Unit.Name)
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
		u := m.observe(r.Unit.Name)
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
	default:
		return "", fmt.Errorf("unknown guest service action %q", r.Action)
	}
}
