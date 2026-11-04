package session

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// BinaryCheckArgument requests a side-effect-free startup check, before CLI
// configuration is initialized. Only a dynamically linked Linux self-copy
// needs this guest loader/library check.
const BinaryCheckArgument = "--session-binary-check"
const BinaryCheckResponse = "yoloctl-session-binary-ok"

// EnsureVMBinary installs the Linux yoloctl artifact for the VM's architecture
// at VMBinaryPath. The full CLI runs session-hold there; the guest binaries
// are built and distributed beside the host CLI by make build. Matching
// content is reused, and replacement preserves already-running holders.
func EnsureVMBinary(ctx context.Context, c *container.Client) error {
	if spaceenv.Native() {
		return spaceenv.RequireSpace(c.Env())
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("session: locate yoloctl binary: %w", err)
	}
	return ensureVMBinaryFrom(ctx, c, self)
}

func ensureVMBinaryFrom(ctx context.Context, c *container.Client, self string) error {
	probeCtx, cancel := context.WithTimeout(ctx, vmhost.GuestProbeTimeout)
	out, err := c.ShellOutput(probeCtx, "sh", "-c", probeVMBinaryScript, "yoloctl-probe", VMBinaryPath)
	cancel()
	if err != nil {
		return fmt.Errorf("session: probe VM binary: %w", err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return fmt.Errorf("session: VM binary probe returned no architecture")
	}
	artifact, err := guestBinary(self, fields[0])
	if err != nil {
		return err
	}
	if len(fields) > 1 && fields[1] == artifact.sum {
		return nil
	}

	// A private staging directory isolates concurrent transfers. The final
	// rename replaces the executable's inode without modifying live holders.
	// Choose the name locally: no parsing of remote mktemp stdout, and never
	// clean up a path that we did not successfully create exclusively.
	stagingDir := "/tmp/yoloctl-session." + rand.Text()
	if err := c.Shell(ctx, "mkdir", "-m", "0700", stagingDir); err != nil {
		return fmt.Errorf("session: prepare binary transfer: %w", err)
	}
	staging := stagingDir + "/yoloctl"
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.ShellQuiet(cleanupCtx, "rm", "-f", staging)
		_ = c.ShellQuiet(cleanupCtx, "rmdir", stagingDir)
	}()
	if err := vmhost.New().CopyTo(ctx, c.Env(), artifact.path, staging); err != nil {
		return fmt.Errorf("session: copy yoloctl into VM: %w", err)
	}
	if artifact.dynamic {
		checkCtx, cancel := context.WithTimeout(ctx, vmhost.GuestProbeTimeout)
		out, err := c.ShellOutput(checkCtx, "sh", "-c", checkVMBinaryScript, "yoloctl-check", staging, artifact.sum, BinaryCheckArgument)
		cancel()
		if err != nil || strings.TrimSpace(out) != BinaryCheckResponse {
			return fmt.Errorf("session: Linux executable cannot run in this VM (loader/library check: %v, response %q); rebuild with CGO_ENABLED=0 go build or make build", err, strings.TrimSpace(out))
		}
	}
	if err := c.Shell(ctx, "sudo", "sh", "-c", installVMBinaryScript, "yoloctl-install", staging, VMBinaryPath, artifact.sum); err != nil {
		return fmt.Errorf("session: install yoloctl in VM: %w", err)
	}
	return nil
}

const probeVMBinaryScript = `set -eu
uname -m
sudo sha256sum "$1" 2>/dev/null || true
`

const checkVMBinaryScript = `set -eu
printf '%s  %s\n' "$2" "$1" | sha256sum -c - >/dev/null
chmod u+x "$1"
exec "$1" "$3"
`

const installVMBinaryScript = `set -eu
install -d -m 0755 "$(dirname "$2")"
tmp=$(mktemp "${2}.XXXXXXXXXX")
trap 'rm -f "$tmp"' EXIT
install -m 0755 "$1" "$tmp"
printf '%s  %s\n' "$3" "$tmp" | sha256sum -c - >/dev/null
mv -f "$tmp" "$2"
`

type guestExecutable struct {
	path, sum string
	dynamic   bool
}

func guestBinary(self, uname string) (guestExecutable, error) {
	artifact, err := guestbinary.Resolve(self, uname)
	return guestExecutable{artifact.Path, artifact.Sum, artifact.Dynamic}, err
}
func sha256File(path string) (string, error) { return guestbinary.SHA256File(path) }
