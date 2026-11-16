package container

import (
	"context"
	"errors"
	"io"
	"strings"

	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// CopyArchive exports the selected retained container, including a stopped
// snapshot. Only a full CID is accepted: removal never switches to a new box.
// The caller verifies ownership immediately before this command. No task is
// started, no mount grant is added, and stdout is the producer's unchanged tar.
func (c *Client) CopyArchive(ctx context.Context, cid, source string, out io.Writer) error {
	if err := ValidateContainerID(cid); err != nil {
		return err
	}
	if source == "" || strings.ContainsRune(source, 0) {
		return errors.New("copy needs a container source path")
	}
	cmd := c.command(ctx, "sudo", "nerdctl", "cp", "--", cid+":"+source, "-")
	cmd.Stdout = out
	return vmhost.RunWithFilteredStderr(cmd, c.stderr)
}
