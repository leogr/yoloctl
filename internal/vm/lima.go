package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
)

type limaDriver struct{ *lima.Client }

func (limaDriver) Kind() string { return "lima" }
func (limaDriver) ValidateInstancePaths(name string) error {
	return lima.ValidateInstancePaths(name)
}
func (d limaDriver) Create(ctx context.Context, name string, spec vmapi.Spec) error {
	return d.Client.Create(ctx, name, lima.TemplateWithBootSettings(spec.Mounts, spec.DiskID, spec.Format, spec.CPUs, spec.Memory, spec.Disk))
}
func (d limaDriver) Stop(ctx context.Context, name string, force bool) error {
	if !force {
		return d.Client.Stop(ctx, name)
	}
	return lima.CommandForSpace(ctx, name, "stop", "--force", "--tty=false", lima.InstanceName(name)).Run()
}
func (limaDriver) Shell(ctx context.Context, name, dir string, args []string) *exec.Cmd {
	if dir != "" {
		return lima.ShellCommandWithWorkdir(ctx, name, dir, args...)
	}
	return lima.ShellCommand(ctx, name, args...)
}
func (limaDriver) InstanceDir(name string) (string, error) { return lima.InstanceDir(name) }
func (limaDriver) ConfigPath(name string) (string, error) {
	d, e := lima.InstanceDir(name)
	return filepath.Join(d, "lima.yaml"), e
}
func (limaDriver) LogPath(name string) (string, error) {
	d, e := lima.InstanceDir(name)
	return filepath.Join(d, "ha.stdout.log"), e
}
func (d limaDriver) SetMounts(name string, m []vmapi.Mount) error {
	return d.SetInstanceMounts(name, m)
}
func (d limaDriver) SetResources(ctx context.Context, name string, r vmapi.Resources) error {
	return d.SetInstanceResources(ctx, name, r)
}
func (limaDriver) AccountDiskDir(id string) (string, error) { return lima.AccountDiskDir(id) }
func (limaDriver) ProcessIDs(name string) ([]int, error)    { return lima.VMProcessIDs(name) }
func (limaDriver) RequireStopped(name, kind string, pids []int) error {
	return lima.RequireVMProcessesStopped(name, kind, pids)
}
func (limaDriver) ControllerService(ctx context.Context, name, action string) *exec.Cmd {
	switch action {
	case "state":
		return lima.ShellCommand(ctx, name, "sudo", "systemctl", "show", "-p", "ActiveState", "--value", "yoloctl-controller.service")
	case "stop":
		return lima.ShellCommand(ctx, name, "sudo", "sh", "-c", `set -eu
state=$(systemctl show -p LoadState --value yoloctl-controller.service)
if [ "$state" != "not-found" ]; then systemctl stop yoloctl-controller.service; fi
`)
	case "start":
		return lima.ShellCommand(ctx, name, "sudo", "systemctl", "start", "yoloctl-controller.service")
	case "install":
		return lima.ShellCommand(ctx, name, "sudo", "/opt/yoloctl/bin/yoloctl", "controller-internal", "install-service")
	default:
		return failedCommand(ctx, fmt.Errorf("unknown controller service action %q", action))
	}
}

// A Firecracker-only installation need not install Lima. Missing Lima is still
// an error if any declared or orphaned Lima VM might exist.
func (d limaDriver) List(ctx context.Context) ([]vmapi.Instance, error) {
	if _, e := exec.LookPath(lima.DefaultBinary); errors.Is(e, exec.ErrNotFound) {
		names, e := spaceconfig.List()
		if e != nil {
			return nil, e
		}
		for _, name := range names {
			b, e := spaceconfig.LoadBootstrap(name)
			if errors.Is(e, spaceconfig.ErrLegacySchema) {
				return nil, errors.New("lima is required for a legacy space")
			}
			if e != nil {
				return nil, e
			}
			if b.VM.Backend == "" || b.VM.Backend == "lima" {
				return nil, errors.New("lima is required for an existing space")
			}
		}
		homes, e := spaceconfig.LimaHomes()
		if e != nil {
			return nil, e
		}
		for _, home := range homes {
			rows, e := os.ReadDir(home)
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return nil, e
			}
			for _, r := range rows {
				if strings.HasPrefix(r.Name(), "yolo-") {
					return nil, errors.New("lima metadata exists; install limactl to inspect it")
				}
			}
		}
		return nil, nil
	}
	return d.Client.List(ctx)
}

func (d limaDriver) InteractiveShell(ctx context.Context, name string, args []string) *exec.Cmd {
	tail := []string{"shell", lima.InstanceName(name)}
	if len(args) > 0 {
		tail = append(tail, "--")
		tail = append(tail, args...)
	}
	return lima.CommandForSpace(ctx, name, tail...)
}
