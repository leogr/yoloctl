package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

var ErrLegacyOpenShellUnavailable = errors.New("recreate this box to move it to the new OpenShell")

// These hashes are READ-ONLY compatibility checks for bytes already installed
// by an older VM. They are not download/install pins: no legacy executable is
// fetched, restored or replaced, and native 0.1.6 bytes cannot satisfy this check.
const retainedLegacyOpenShellCheck = `set -eu
binary=/opt/openshell/bin/openshell-sandbox
if [ ! -f "$binary" ] || [ -L "$binary" ] || [ ! -x "$binary" ]; then exit 1; fi
case "$(uname -m)" in
 x86_64) expected=f9f991a24d10772ad5d24ae27a8ea6baad8cac671695bd90fcd0355e0e0ad198 ;;
 aarch64) expected=32ca44fe7d9e6d332f2a753c6b8a1a6117b7388281dad9b5274d23ffc67e216f ;;
 *) exit 1 ;;
esac
printf '%s  %s\n' "$expected" "$binary" | sha256sum --check --status
`

// CheckRetainedLegacyOpenShell reads ONLY the fixed historical VM executable.
// The caller has independently selected an actual legacy container launch.
// Cancellation/failed reads never permit launch or trigger installation.
func (c *Client) CheckRetainedLegacyOpenShell(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrLegacyOpenShellUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	read, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := c.command(read, "sudo", "sh", "-c", retainedLegacyOpenShellCheck)
	// Discard all output instead of capturing guest/environment data. Pipe wait
	// settlement is separately bounded if an adapter descendant retains a pipe.
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: recorded legacy runtime is unavailable: %w", ErrLegacyOpenShellUnavailable, errors.Join(err, read.Err()))
	}
	if err := read.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrLegacyOpenShellUnavailable, err)
	}
	return nil
}
