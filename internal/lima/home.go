package lima

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// InstancePrefix namespaces every Lima instance yoloctl manages so its
// VMs are visually distinct from any hand-rolled Lima instances the user
// runs. A yoloctl space named "default" maps to the Lima instance
// "yolo-default". The prefix is an internal detail: the rest of yoloctl
// speaks space names, and List strips it back off on the way out.
const InstancePrefix = "yolo-"

// InstanceName maps a yoloctl space name to its backing Lima instance
// name (the identifier passed to limactl).
func InstanceName(space string) string { return InstancePrefix + space }

// spaceFromInstance reverses InstanceName. ok is false for instances that
// aren't yoloctl-managed (missing the prefix), letting List ignore any
// foreign VMs that happen to share the LIMA_HOME.
func spaceFromInstance(instance string) (space string, ok bool) {
	if !strings.HasPrefix(instance, InstancePrefix) {
		return "", false
	}
	return strings.TrimPrefix(instance, InstancePrefix), true
}

// SetupHome initializes the legacy default at startup. Space/disk commands
// override LIMA_HOME in their child environment from the persisted bootstrap;
// no existing storage is moved or searched as a replacement for missing data.
func SetupHome() error {
	if spaceenv.Native() {
		return nil
	}
	home, err := layout.LimaHome()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create lima home %s: %w", home, err)
	}
	return os.Setenv("LIMA_HOME", home)
}

// InstanceDir returns the on-disk directory limactl uses for the named
// space's instance (selected home/yolo-<space>). Resolution never creates or
// moves storage; callers that need existence checks stat the result.
func InstanceDir(space string) (string, error) {
	home, err := spaceconfig.LimaHome(space)
	if err != nil {
		return "", err
	}
	return layout.LimaInstanceDir(home, space)
}

// Command builds an *exec.Cmd for `limactl <args...>`. Every yoloctl
// limactl invocation should originate here (or via ShellCommand) so the
// binary name lives in one place. Use CommandForSpace for VM operations;
// Command without a target is only for process-global queries such as --version.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	if spaceenv.Native() {
		return exec.CommandContext(ctx, "sh", "-c", "echo 'host VM administration is unavailable inside the controller' >&2; exit 2")
	}
	return exec.CommandContext(ctx, DefaultBinary, args...)
}

// CommandForSpace overrides LIMA_HOME only in this child. A single process may
// operate legacy and newly placed spaces concurrently without global env races.
func CommandForSpace(ctx context.Context, space string, args ...string) *exec.Cmd {
	return New().spaceCommand(ctx, space, args...)
}

func failedCommand(ctx context.Context, err error) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", `printf '%s\n' "$1" >&2; exit 2`, "lima-error", err.Error())
}

func (c *Client) commandAtHome(ctx context.Context, home string, args ...string) *exec.Cmd {
	if spaceenv.Native() {
		return failedCommand(ctx, errors.New("VM controller cannot execute host Lima operations"))
	}
	cmd := exec.CommandContext(ctx, c.binary, args...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "LIMA_HOME=") {
			env = append(env, value)
		}
	}
	cmd.Env = append(env, "LIMA_HOME="+home)
	return cmd
}

func (c *Client) spaceCommand(ctx context.Context, space string, args ...string) *exec.Cmd {
	home, err := spaceconfig.LimaHome(space)
	if err != nil {
		return failedCommand(ctx, err)
	}
	return c.commandAtHome(ctx, home, args...)
}

func (c *Client) diskCommand(ctx context.Context, id string, args ...string) *exec.Cmd {
	home, err := spaceconfig.LimaHomeForDisk(id)
	if err != nil {
		return failedCommand(ctx, err)
	}
	return c.commandAtHome(ctx, home, args...)
}

// AccountDiskDir uses the same placement as disk inspection/allocation/removal.
func AccountDiskDir(id string) (string, error) {
	home, err := spaceconfig.LimaHomeForDisk(id)
	if err != nil {
		return "", err
	}
	return layout.LimaAccountDiskDir(home, id)
}

// ShellCommand builds `limactl shell <instance> -- args...` for the given
// space, applying the yolo- instance prefix. Callers set stdio on the
// returned command.
func ShellCommand(ctx context.Context, space string, args ...string) *exec.Cmd {
	if spaceenv.Native() {
		return localCommand(ctx, space, "", args...)
	}
	full := append([]string{"shell", InstanceName(space), "--"}, args...)
	return CommandForSpace(ctx, space, full...)
}

// ShellCommandWithWorkdir is ShellCommand with a remote --workdir, which
// limactl requires positioned before the instance name.
func ShellCommandWithWorkdir(ctx context.Context, space, workdir string, args ...string) *exec.Cmd {
	if spaceenv.Native() {
		return localCommand(ctx, space, workdir, args...)
	}
	full := append([]string{"shell", "--workdir", workdir, InstanceName(space), "--"}, args...)
	return CommandForSpace(ctx, space, full...)
}

func localCommand(ctx context.Context, space, workdir string, args ...string) *exec.Cmd {
	if err := spaceenv.RequireSpace(space); err != nil {
		return exec.CommandContext(ctx, "sh", "-c", "echo \"$1\" >&2; exit 2", "scope", err.Error())
	}
	if len(args) == 0 {
		return exec.CommandContext(ctx, "false")
	}
	// Controller workers are already privileged; sudo would discard context.
	if args[0] == "sudo" {
		args = args[1:]
	}
	if len(args) == 0 {
		return exec.CommandContext(ctx, "false")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = workdir
	return cmd
}
