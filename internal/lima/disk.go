package lima

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/vmapi"
)

// DiskInfo is Lima's independently managed data disk, not a VM system disk.
type DiskInfo = vmapi.DiskInfo

func (c *Client) AccountDisk(ctx context.Context, id string) (*DiskInfo, error) {
	name := layout.AccountDiskName(id)
	cmd := c.diskCommand(ctx, id, "disk", "list", "--json", name)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inspect account disk: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var disk DiskInfo
		if err := decoder.Decode(&disk); errors.Is(err, io.EOF) {
			return nil, nil
		} else if err != nil {
			return nil, fmt.Errorf("decode account disk: %w", err)
		}
		if disk.Name == name {
			return &disk, nil
		}
	}
}

func (c *Client) RequireAccountDisk(ctx context.Context, space, id string) error {
	d, err := c.AccountDisk(ctx, id)
	if err != nil {
		return err
	}
	if d == nil {
		return fmt.Errorf("account disk %s is missing or unreadable; refusing to create an empty replacement", layout.AccountDiskName(id))
	}
	if d.Instance != "" && d.Instance != InstanceName(space) {
		return fmt.Errorf("account disk is attached to another VM: %s", d.Instance)
	}
	return nil
}
func (c *Client) CreateAccountDisk(ctx context.Context, id string, size uint64) error {
	if size == 0 || size%(1<<30) != 0 {
		return fmt.Errorf("data disk size %d is not whole GiB", size)
	}
	home, err := spaceconfig.LimaHomeForDisk(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	return c.runDisk(ctx, id, "disk", "create", layout.AccountDiskName(id), "--size", fmt.Sprintf("%dGiB", size>>30), "--format", "raw")
}
func (c *Client) DeleteAccountDisk(ctx context.Context, id string) error {
	if err := c.runDisk(ctx, id, "disk", "delete", layout.AccountDiskName(id)); err != nil {
		return err
	}
	// Lima can log a failed disk inspection and still exit successfully.
	// Keep the declaration until physical deletion is established.
	dir, err := AccountDiskDir(id)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("verify account disk deletion: %w", err)
	}
	return fmt.Errorf("account disk %s still exists", layout.AccountDiskName(id))
}

// SetAccountDisk updates the owned disk; format may only be true for an
// explicitly pending initialization after proving its guest-visible data is blank.
func (c *Client) SetAccountDisk(space, id string, format bool) error {
	path, err := InstanceConfigPath(space)
	if err != nil {
		return err
	}
	return setInstanceAccountDisk(path, id, format)
}
