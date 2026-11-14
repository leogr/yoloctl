package vmservice

import (
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
		if got := strings.Join(logArgv(tc.native, "yoloctl-session-coder.service", 50, tc.follow), " "); got != tc.want {
			t.Fatalf("native %v follow %v: %q", tc.native, tc.follow, got)
		}
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
	for _, name := range []string{vmwire.AuditUnitName, "yoloctl-session-coder.service", "yoloctl-port-coder-8080.service", "yoloctl-openshell-port-coder-3000.service"} {
		if _, err := LogCommand(c, name, 10, false); err != nil {
			t.Fatalf("%q refused: %v", name, err)
		}
	}
}
