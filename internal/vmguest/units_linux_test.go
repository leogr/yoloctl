//go:build linux

package vmguest

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

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
