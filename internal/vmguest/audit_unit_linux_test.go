//go:build linux

package vmguest

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/vmwire"
)

func TestAuditUnitHasClosedLauncherAndReadOnlyCapability(t *testing.T) {
	valid := vmwire.AuditUnit()
	if err := validateUnit(valid, true); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*vmwire.Unit){
		func(u *vmwire.Unit) { u.Name += ".service" },
		func(u *vmwire.Unit) { u.Args[0] = "/bin/sh" },
		func(u *vmwire.Unit) { u.Args = append(u.Args, "--command", "id") },
		func(u *vmwire.Unit) { u.Args[2] = "worker" },
		func(u *vmwire.Unit) { u.Restart = true },
		func(u *vmwire.Unit) { u.Env = nil },
		func(u *vmwire.Unit) { u.Env = append(u.Env, "LD_PRELOAD=/tmp/evil.so") },
		func(u *vmwire.Unit) { u.Env = append(u.Env, "HOME=/root") },
	} {
		unit := valid
		unit.Args = append([]string(nil), valid.Args...)
		unit.Env = append([]string(nil), valid.Env...)
		change(&unit)
		if err := validateUnit(unit, true); err == nil {
			t.Fatal("accepted altered audit launcher", unit)
		}
	}
	unitManager.Lock()
	before := len(unitManager.units)
	unitManager.Unlock()
	reply, err := manageUnit(vmwire.UnitRequest{Action: vmwire.AuditCapabilityAction, Unit: vmwire.Unit{Name: vmwire.AuditUnitName}})
	if err != nil || reply != vmwire.AuditCapability {
		t.Fatal(reply, err)
	}
	unitManager.Lock()
	after := len(unitManager.units)
	unitManager.Unlock()
	if after != before {
		t.Fatal("capability query mutated managed services")
	}
	for _, unit := range []vmwire.Unit{{Name: "other"}, {Name: vmwire.AuditUnitName, Args: []string{"id"}}, {Name: vmwire.AuditUnitName, Restart: true}} {
		if _, err := manageUnit(vmwire.UnitRequest{Action: vmwire.AuditCapabilityAction, Unit: unit}); err == nil {
			t.Fatal("unbounded capability query", unit)
		}
	}
	if err := validateUnit(vmwire.Unit{Name: strings.Repeat("x", 200)}, false); err == nil {
		t.Fatal("broadened ordinary service identities")
	}
}
