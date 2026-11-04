package lima

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
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

// SetupHome points limactl at yoloctl's private Lima home for the rest of the
// process by setting LIMA_HOME (creating the directory if needed).
// Call once at startup, before any limactl invocation: every exec of
// limactl inherits the process environment, so this single call relocates
// all of yoloctl's Lima instances. Idempotent.
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
// space's instance (LimaHome/<yolo-space>). Pure path construction — does not
// check existence; callers that need that stat the result.
func InstanceDir(space string) (string, error) {
	home, err := layout.LimaHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, InstanceName(space)), nil
}

// Command builds an *exec.Cmd for `limactl <args...>`. Every yoloctl
// limactl invocation should originate here (or via ShellCommand) so the
// binary name lives in one place; LIMA_HOME is carried by the process
// environment set up in SetupHome.
func Command(ctx context.Context, args ...string) *exec.Cmd {
	if spaceenv.Native() {
		return exec.CommandContext(ctx, "sh", "-c", "echo 'host VM administration is unavailable inside the controller' >&2; exit 2")
	}
	return exec.CommandContext(ctx, DefaultBinary, args...)
}

// ShellCommand builds `limactl shell <instance> -- args...` for the given
// space, applying the yolo- instance prefix. Callers set stdio on the
// returned command.
func ShellCommand(ctx context.Context, space string, args ...string) *exec.Cmd {
	if spaceenv.Native() {
		return localCommand(ctx, space, "", args...)
	}
	full := append([]string{"shell", InstanceName(space), "--"}, args...)
	return Command(ctx, full...)
}

// ShellCommandWithWorkdir is ShellCommand with a remote --workdir, which
// limactl requires positioned before the instance name.
func ShellCommandWithWorkdir(ctx context.Context, space, workdir string, args ...string) *exec.Cmd {
	if spaceenv.Native() {
		return localCommand(ctx, space, workdir, args...)
	}
	full := append([]string{"shell", "--workdir", workdir, InstanceName(space), "--"}, args...)
	return Command(ctx, full...)
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
