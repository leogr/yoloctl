// Package vmservice manages independent guest infrastructure services. Both
// supervisors preserve fixed-name exclusion and never restart session holders.
package vmservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vm"
	"github.com/leogr/yoloctl/internal/vmwire"
)

type Unit = vmwire.Unit

// LogHint describes the selected guest supervisor's diagnostic surface.
func LogHint(c *container.Client, name string) string {
	if native, err := microvm(c); err == nil && native {
		return "cat /var/log/yoloctl-units/" + name + ".log"
	}
	return "journalctl -u " + name
}

func microvm(c *container.Client) (bool, error) {
	if current := spaceenv.Current(); current != nil {
		return current.ServiceManager == "microvm", spaceenv.RequireSpace(c.Env())
	}
	b, e := spaceconfig.LoadBootstrap(c.Env())
	if errors.Is(e, os.ErrNotExist) || errors.Is(e, spaceconfig.ErrLegacySchema) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return b.VM.Backend == "firecracker", nil
}
func request(ctx context.Context, c *container.Client, r vmwire.UnitRequest) (string, error) {
	raw, e := json.Marshal(r)
	if e != nil {
		return "", e
	}
	cmd := vm.ShellCommand(ctx, c.Env(), "sudo", "/sbin/yoloctl-init", "unit")
	cmd.Stdin = bytes.NewReader(raw)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if e = cmd.Run(); e != nil {
		return "", fmt.Errorf("guest service: %w: %s", e, stderr.String())
	}
	return strings.TrimSpace(out.String()), nil
}
func Start(ctx context.Context, c *container.Client, u Unit) error {
	native, e := microvm(c)
	if e != nil {
		return e
	}
	if native {
		_, e = request(ctx, c, vmwire.UnitRequest{Action: "start", Unit: u})
		return e
	}
	args := []string{"sudo", "systemd-run", "--unit", u.Name, "--description", u.Description}
	if !strings.HasPrefix(u.Name, vmwire.OpenShellPortUnitPrefix) {
		args = append(args, "--collect")
	}
	// Keep a failed new forward addressable until its acknowledged stop;
	// automatic failed-unit collection would discard that ownership first.
	args = append(args, "--quiet")
	if u.Name == vmwire.AuditUnitName {
		args = append(args, "-p", "KillMode=control-group", "-p", "TimeoutStopSec=10", "-p", "UMask=0077")
	}
	if u.Restart {
		args = append(args, "-p", "Restart=on-failure")
	}
	for _, env := range u.Env {
		args = append(args, "--setenv="+env)
	}
	args = append(args, "--")
	args = append(args, u.Args...)
	return c.Shell(ctx, args...)
}
func Stop(ctx context.Context, c *container.Client, name string) error {
	native, e := microvm(c)
	if e != nil {
		return e
	}
	if native {
		_, e = request(ctx, c, vmwire.UnitRequest{Action: "stop", Unit: Unit{Name: name}})
		return e
	}
	if e = c.ShellQuiet(ctx, "sudo", "systemctl", "stop", name); e != nil {
		state, probe := State(ctx, c, name)
		if probe == nil && (state == "inactive" || state == "failed" || state == "unknown") {
			return nil
		}
		return e
	}
	return nil
}

// StopAcknowledged preserves the actual stop result. A later state observation
// cannot replace a failed/lost acknowledgement or prove complete cleanup. New
// native creation uses this path; legacy idempotent Stop remains unchanged.
func StopAcknowledged(ctx context.Context, c *container.Client, name string) error {
	native, err := microvm(c)
	if err != nil {
		return err
	}
	if native {
		_, err = request(ctx, c, vmwire.UnitRequest{Action: "stop", Unit: Unit{Name: name}})
		return err
	}
	return c.ShellQuiet(ctx, "sudo", "systemctl", "stop", name)
}

// Collect drops a stopped transient unit that systemd retained in the failed
// state (units started without --collect), so a later inventory no longer
// lists it. Best effort: a unit already gone is not an error. The microVM unit
// manager collects stopped units itself.
func Collect(ctx context.Context, c *container.Client, name string) error {
	native, err := microvm(c)
	if err != nil || native {
		return err
	}
	_ = c.ShellQuiet(ctx, "sudo", "systemctl", "reset-failed", name)
	return nil
}

func State(ctx context.Context, c *container.Client, name string) (string, error) {
	native, e := microvm(c)
	if e != nil {
		return "", e
	}
	if native {
		return request(ctx, c, vmwire.UnitRequest{Action: "state", Unit: Unit{Name: name}})
	}
	// is-active exits nonzero for stopped/missing units. Preserve its state text
	// so missing is distinguishable from a broken transport or service manager.
	cmd := vm.ShellCommand(ctx, c.Env(), "sudo", "systemctl", "is-active", name)
	raw, e := cmd.Output()
	state := strings.TrimSpace(string(raw))
	if state == "inactive" || state == "failed" || state == "unknown" {
		return state, nil
	}
	return state, e
}
func List(ctx context.Context, c *container.Client, prefix string) (string, error) {
	native, e := microvm(c)
	if e != nil {
		return "", e
	}
	if native {
		return request(ctx, c, vmwire.UnitRequest{Action: "list", Unit: Unit{Name: prefix}})
	}
	if prefix == vmwire.LegacyPortUnitPrefix {
		return InventoryPorts(ctx, c)
	}
	return c.ShellOutput(ctx, "sudo", "systemctl", "list-units", "--no-legend", "--plain", "--type=service", "--state=running", prefix+"*")
}

var auditCapabilityRequest = request

// AuditSupported is a read-only compatibility query. Older init images reject
// the unknown action before any service effect; ordinary units are unchanged.
func AuditSupported(ctx context.Context, c *container.Client) error {
	native, err := microvm(c)
	if err != nil {
		return err
	}
	if !native {
		return nil
	}
	bounded, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	answer, err := auditCapabilityRequest(bounded, c, vmwire.UnitRequest{Action: vmwire.AuditCapabilityAction, Unit: Unit{Name: vmwire.AuditUnitName}})
	if err != nil {
		return fmt.Errorf("cannot confirm Firecracker audit service support (older images need explicit recreation with --update-vm-image): %w", err)
	}
	if answer != vmwire.AuditCapability {
		return errors.New("the running Firecracker image lacks audit service support; explicitly recreate the space with --update-vm-image")
	}
	return nil
}
