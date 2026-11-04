//go:build linux

package vmguest

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestManagedHolderInventoryRetainsStoppingFailedAndInactiveUnits(t *testing.T) {
	// Fictional service-manager objects only: no processes/cgroups/VM launched.
	const prefix = "yoloctl-session-"
	active := &managedUnit{cmd: &exec.Cmd{Process: &os.Process{Pid: 4242}}}
	unitManager.Lock()
	saved := unitManager.units
	unitManager.units = map[string]*managedUnit{
		prefix + "active.service":         active,
		prefix + "stopping.service":       {stopping: true},
		prefix + "failed.service":         {failed: errors.New("fixture exit")},
		prefix + "inactive.service":       {},
		"yoloctl-port-peer-80-80.service": {},
	}
	unitManager.Unlock()
	t.Cleanup(func() { unitManager.Lock(); unitManager.units = saved; unitManager.Unlock() })
	got, err := manageUnit(vmwire.UnitRequest{Action: "inventory", Unit: vmwire.Unit{Name: prefix}})
	want := strings.Join([]string{prefix + "active.service", prefix + "failed.service", prefix + "inactive.service", prefix + "stopping.service"}, "\n")
	if err != nil || got != want {
		t.Fatalf("complete holder inventory omitted retained units or included peer transport: %q %v", got, err)
	}
	listed, err := manageUnit(vmwire.UnitRequest{Action: "list", Unit: vmwire.Unit{Name: prefix}})
	if err != nil || listed != prefix+"active.service" {
		t.Fatalf("fixture did not distinguish active-only inventory: %q %v", listed, err)
	}
}
