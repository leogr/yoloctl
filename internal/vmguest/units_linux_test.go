//go:build linux

package vmguest

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestManagedServiceLaunchBoundary(t *testing.T) {
	valid := vmwire.Unit{Name: "yoloctl-session-probe.service", Args: []string{vmwire.GuestBinary, "session-hold", "probe"}, Env: []string{"HOME=/root", "YOLOCTL_CONTROLLER_CONTEXT=/etc/yoloctl/controller.json"}}
	if e := validateUnit(valid, true); e != nil {
		t.Fatal(e)
	}
	tests := []vmwire.Unit{
		{Name: "../controller.service", Args: valid.Args},
		{Name: valid.Name, Args: []string{"/bin/sh", "-c", "true"}},
		{Name: valid.Name, Args: valid.Args, Restart: true},
		{Name: valid.Name, Args: valid.Args, Env: []string{"LD_PRELOAD=/tmp/lib.so"}},
		{Name: "yoloctl-port-test.service", Args: []string{"/usr/bin/socat", "TCP-LISTEN:8000", "EXEC:/bin/sh"}, Restart: true},
	}
	for _, u := range tests {
		if e := validateUnit(u, true); e == nil {
			t.Fatalf("accepted unsupported service: %+v", u)
		}
	}
}

func TestManagedOpenShellForwardLaunchBoundary(t *testing.T) {
	spec := port.Spec{Agentbox: "web", HostPort: 8000, ContainerPort: 3000}
	binding := port.ForwardBinding{ContainerID: strings.Repeat("1", 64), Incarnation: strings.Repeat("2", 32), Qualification: strings.Repeat("3", 64), HelperSHA256: strings.Repeat("4", 64), Generation: "g" + strings.Repeat("5", 48), RecordSHA256: strings.Repeat("6", 64), Attempt: strings.Repeat("7", 32)}
	args, err := port.ForwardWorkerArgs(spec, binding)
	if err != nil {
		t.Fatal(err)
	}
	name, err := port.OpenShellUnitName(spec)
	if err != nil {
		t.Fatal(err)
	}
	valid := vmwire.Unit{Name: name, Args: append([]string{vmwire.GuestBinary}, args...), Restart: true, Env: []string{"HOME=/root", "YOLOCTL_CONTROLLER_CONTEXT=/etc/yoloctl/controller.json"}}
	if err := validateUnit(valid, true); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*vmwire.Unit){
		func(u *vmwire.Unit) { u.Name = "yoloctl-openshell-port-other-8000-3000.service" },
		func(u *vmwire.Unit) { u.Args[0] = "/tmp/yoloctl" },
		func(u *vmwire.Unit) { u.Args[1] = "exec" },
		func(u *vmwire.Unit) { u.Args = append(u.Args, "--command", "/bin/sh") },
		func(u *vmwire.Unit) { u.Args[5] = "08000" },
		func(u *vmwire.Unit) { u.Restart = false },
		func(u *vmwire.Unit) { u.Env = nil },
		func(u *vmwire.Unit) { u.Env = append(u.Env, "HOME=/root") },
		func(u *vmwire.Unit) { u.Env = append(u.Env, "HTTP_PROXY=untrusted") },
	}
	for i, mutate := range mutations {
		changed := valid
		changed.Args, changed.Env = append([]string(nil), valid.Args...), append([]string(nil), valid.Env...)
		mutate(&changed)
		if err := validateUnit(changed, true); err == nil {
			t.Fatalf("accepted launcher mutation %d: %+v", i, changed)
		}
	}
}

// The in-memory supervisor observations below never start a process, touch a
// cgroup or invoke a VM. PID is deliberately fictional.
func TestManagedServiceProcessObservesRestartAndExitWithoutStalePID(t *testing.T) {
	const name = "yoloctl-openshell-port-web-8000-3000.service"
	unitManager.Lock()
	saved := unitManager.units
	u := &managedUnit{cmd: &exec.Cmd{Process: &os.Process{Pid: 4242}}, done: make(chan struct{})}
	unitManager.units = map[string]*managedUnit{name: u}
	unitManager.Unlock()
	t.Cleanup(func() { unitManager.Lock(); unitManager.units = saved; unitManager.Unlock() })
	observe := func(state string, pid int) {
		t.Helper()
		raw, err := manageUnit(vmwire.UnitRequest{Action: "process", Unit: vmwire.Unit{Name: name}})
		var got vmwire.UnitProcess
		if err != nil || json.Unmarshal([]byte(raw), &got) != nil || got.Name != name || got.State != state || got.PID != pid {
			t.Fatalf("want %s/%d, got %+v, %v", state, pid, got, err)
		}
	}
	observe("active", 4242)
	u.mu.Lock()
	u.cmd = nil
	u.mu.Unlock()
	observe("activating", 0)
	u.mu.Lock()
	u.failed = errors.New("fictional worker exit")
	close(u.done)
	u.mu.Unlock()
	observe("failed", 0)
	unitManager.Lock()
	delete(unitManager.units, name)
	unitManager.Unlock()
	observe("inactive", 0)
}

func TestManagedForwardInventoryIncludesRestartGapsAndRetainsBothPrefixes(t *testing.T) {
	// Entirely fictional in-memory supervisor state, no process/cgroup/VM.
	legacy := "yoloctl-port-old-8000-80.service"
	native := "yoloctl-openshell-port-new-9000-90.service"
	gap := "yoloctl-openshell-port-gap-9100-91.service"
	failed := "yoloctl-port-failed-9200-92.service"
	active := func() *managedUnit { return &managedUnit{cmd: &exec.Cmd{Process: &os.Process{Pid: 4242}}} }
	unitManager.Lock()
	saved := unitManager.units
	unitManager.units = map[string]*managedUnit{legacy: active(), native: active(), gap: {}, failed: {failed: errors.New("fictional exit")}}
	unitManager.Unlock()
	t.Cleanup(func() { unitManager.Lock(); unitManager.units = saved; unitManager.Unlock() })
	listed, err := manageUnit(vmwire.UnitRequest{Action: "inventory", Unit: vmwire.Unit{Name: vmwire.LegacyPortUnitPrefix}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{legacy, native, gap, failed} {
		if !strings.Contains("\n"+listed+"\n", "\n"+name+"\n") {
			t.Fatal("known unit omitted from complete inventory", name, listed)
		}
	}
	managed, err := manageUnit(vmwire.UnitRequest{Action: "list", Unit: vmwire.Unit{Name: vmwire.LegacyPortUnitPrefix}})
	if err != nil || managed != listed {
		t.Fatal("managed view omitted a retained/restarting port service", managed, err)
	}
}

func TestManagedStopOrdersAgainstPendingStart(t *testing.T) {
	const name = "yoloctl-session-pending.service"
	for _, previous := range []bool{false, true} {
		for _, fails := range []bool{false, true} {
			synctest.Test(t, func(t *testing.T) {
				starting, release := make(chan struct{}), make(chan struct{})
				launchErr := errors.New("fixture preparation failure")
				m := unitRegistry{
					units: make(map[string]*managedUnit),
					names: make(map[string]*unitName),
					clean: func(string) error { return nil },
					launch: func(u *managedUnit) error {
						close(starting)
						<-release
						if fails {
							return launchErr
						}
						cmd := exec.Command("sleep", "60")
						if err := startOwned(cmd); err != nil {
							return err
						}
						u.cmd = cmd
						t.Cleanup(func() { _ = cmd.Process.Kill() })
						return nil
					},
				}
				if previous {
					u := &managedUnit{spec: vmwire.Unit{Name: name}, stop: make(chan struct{}), done: make(chan struct{})}
					close(u.done)
					m.units[name] = u
				}
				started, stopped := make(chan error, 1), make(chan error, 1)
				go func() {
					_, err := m.manage(vmwire.UnitRequest{Action: "start", Unit: vmwire.Unit{
						Name: name, Args: []string{vmwire.GuestBinary, "session-hold", "fixture"},
					}})
					started <- err
				}()
				<-starting
				go func() {
					_, err := m.manage(vmwire.UnitRequest{Action: "stop", Unit: vmwire.Unit{Name: name}})
					stopped <- err
				}()
				synctest.Wait()
				select {
				case err := <-stopped:
					t.Errorf("stop acknowledged before preparation settled (previous=%t, fails=%t): %v", previous, fails, err)
					stopped <- err
				default:
				}
				roster, err := m.manage(vmwire.UnitRequest{Action: "inventory", Unit: vmwire.Unit{Name: "yoloctl-session-"}})
				if err != nil || roster != name {
					t.Errorf("pending name must appear once: %q, %v", roster, err)
				}
				close(release)
				if err := <-started; (fails && !errors.Is(err, launchErr)) || (!fails && err != nil) {
					t.Errorf("start (fails=%t): %v", fails, err)
				}
				if err := <-stopped; err != nil || m.lookup(name) != nil || len(m.names) != 0 {
					t.Errorf("stop failed to retire the selected unit and release the reservation: %v", err)
				}
			})
		}
	}
}

func TestManagedStopCleanupDoesNotTouchReplacement(t *testing.T) {
	spec := vmwire.Unit{Name: "yoloctl-session-pending.service"}
	old := &managedUnit{spec: spec}
	replacement := &managedUnit{spec: spec}
	m := unitRegistry{
		units: map[string]*managedUnit{spec.Name: replacement},
		names: make(map[string]*unitName),
		clean: func(string) error { t.Error("stale cleanup signaled a replacement"); return nil },
	}
	for _, remove := range []bool{false, true} {
		if err := m.cleanup(old, remove); err != nil || m.lookup(spec.Name) != replacement {
			t.Fatalf("stale cleanup changed the selected unit: %v", err)
		}
	}
}

func TestManagedPortWithdrawalCanPassStalledRestart(t *testing.T) {
	const a = "yoloctl-port-alpha-8000-80.service"
	const b = "yoloctl-port-beta-9000-90.service"
	t.Run("complete inventory", func(t *testing.T) {
		restarting, release := make(chan struct{}), make(chan struct{})
		cmd := exec.Command("false")
		if err := startOwned(cmd); err != nil {
			t.Fatal(err)
		}
		alpha := &managedUnit{spec: vmwire.Unit{Name: a, Restart: true}, cmd: cmd, stop: make(chan struct{}), done: make(chan struct{})}
		beta := &managedUnit{spec: vmwire.Unit{Name: b}, stop: make(chan struct{}), done: make(chan struct{})}
		close(beta.done)
		m := unitRegistry{
			units: map[string]*managedUnit{a: alpha, b: beta},
			names: make(map[string]*unitName),
			clean: func(string) error { return nil },
			launch: func(u *managedUnit) error {
				close(restarting)
				<-release
				return errors.New("fixture restart aborted")
			},
		}
		go m.watch(alpha)
		<-restarting
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			for _, action := range []string{"inventory", "list"} {
				got, err := m.manage(vmwire.UnitRequest{Action: action, Unit: vmwire.Unit{Name: vmwire.LegacyPortUnitPrefix}})
				if err != nil || got != a+"\n"+b {
					t.Errorf("%s must include both boxes: %q, %v", action, got, err)
				}
			}
			if _, err := m.manage(vmwire.UnitRequest{Action: "stop", Unit: vmwire.Unit{Name: b}}); err != nil {
				t.Error(err)
			}
			got, err := m.manage(vmwire.UnitRequest{Action: "inventory", Unit: vmwire.Unit{Name: vmwire.LegacyPortUnitPrefix}})
			if err != nil || got != a || m.lookup(a) != alpha {
				t.Errorf("beta stop must leave alpha alone: %q, %v", got, err)
			}
		}()
		// A failure here means the inventory waited on alpha's held mutex.
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Error("beta withdrawal waited for alpha's restart")
		}
		close(release)
		<-finished
		<-alpha.done
	})
}
