package vmservice

import (
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/vmwire"
)

// A unit log is read from the unit's file in a microVM, followed by name,
// and from its journal elsewhere, message text only.
func TestLogArgv(t *testing.T) {
	for _, tc := range []struct {
		native, follow bool
		want           string
	}{
		{true, false, "sudo tail -n 50 /var/log/yoloctl-units/yoloctl-session-coder.service.log"},
		{true, true, "sudo tail -n 50 -F /var/log/yoloctl-units/yoloctl-session-coder.service.log"},
		{false, false, "sudo journalctl --no-pager -o cat -u yoloctl-session-coder.service -n 50"},
		{false, true, "sudo journalctl --no-pager -o cat -u yoloctl-session-coder.service -n 50 -f"},
	} {
		argv, err := logArgv(tc.native, "yoloctl-session-coder.service", 50, tc.follow)
		if got := strings.Join(argv, " "); err != nil || got != tc.want {
			t.Fatalf("native %v follow %v: %q %v", tc.native, tc.follow, got, err)
		}
	}
}

// The controller's unit is read from its journal in a Lima guest; a
// microVM's controller writes to the VM log, so --unit points there.
func TestLogArgvController(t *testing.T) {
	argv, err := logArgv(false, vmwire.ControllerUnitName, 20, true)
	if got := strings.Join(argv, " "); err != nil || got != "sudo journalctl --no-pager -o cat -u yoloctl-controller.service -n 20 -f" {
		t.Fatalf("Lima controller argv %q %v", got, err)
	}
	if _, err := logArgv(true, vmwire.ControllerUnitName, 20, false); !errors.Is(err, ErrControllerInVMLog) {
		t.Fatalf("microVM controller: %v", err)
	}
}

// Only managed unit names are read, with no character a guest shell could
// interpret: a refusal comes before any VM or bootstrap access.
func TestLogCommandRefusesOtherNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := container.New("fixture")
	for _, name := range []string{"", "containerd", "yoloctl-session-coder", "../yoloctl-session-x.service", "yoloctl-session-a;reboot.service",
		"yoloctl-session-a b.service", "yoloctl-session-$(id).service", "yoloctl-port-x\n.service"} {
		if _, err := LogCommand(c, name, 10, false); err == nil {
			t.Fatalf("%q accepted", name)
		}
	}
	for _, name := range []string{vmwire.AuditUnitName, vmwire.ControllerUnitName, "yoloctl-session-coder.service", "yoloctl-port-coder-8080.service", "yoloctl-openshell-port-coder-3000.service"} {
		if _, err := LogCommand(c, name, 10, false); err != nil {
			t.Fatalf("%q refused: %v", name, err)
		}
	}
}
