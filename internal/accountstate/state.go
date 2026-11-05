// Package accountstate administers native state inside the owning space VM.
package accountstate

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

//go:embed guest.py
var guestScript string

// Private selected-command seam; production retains the shared VM adapter.
var accountStateCommand = vmhost.ShellCommand

type Request struct {
	Action  string `json:"action"`
	Account string `json:"account,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Path    string `json:"path,omitempty"`
	Data    []byte `json:"data,omitempty"`
	Mount   string `json:"mount"`
	Root    string `json:"root"`
	ID      string `json:"id"`
	UUID    string `json:"uuid"`
	UID     int    `json:"uid"`
	GID     int    `json:"gid"`
}
type AuthFinding struct {
	Source  string `json:"source"`
	Outcome string `json:"outcome"`
}

type AuthStatusSpec struct {
	Format      string `json:"format"`
	ProfilesDir string `json:"profiles_dir,omitempty"`
}

type Result struct {
	Auth    []AuthFinding `json:"auth,omitempty"`
	UUID    string        `json:"uuid"`
	Present bool          `json:"present"`
	Leaves  []string      `json:"leaves"`
	// Outcome of a statusline action: enabled, restored, removed, unchanged
	// or absent.
	Outcome string `json:"outcome,omitempty"`
}

// StatusLine is the Data of a statusline action: set Command as the
// statusLine of the JSON settings file at Path (Enable), or restore the one
// it replaced. The file's contents never leave the VM.
type StatusLine struct {
	Enable  bool   `json:"enable"`
	Command string `json:"command"`
}

func Execute(ctx context.Context, space string, req Request) (Result, error) {
	var result Result
	cfg, err := spaceconfig.Load(space)
	if err != nil {
		return result, err
	}
	if req.Account != "" {
		if err := layout.ValidateName("account", req.Account); err != nil {
			return result, err
		}
	}
	if req.Agent != "" {
		if err := layout.ValidateName("agent", req.Agent); err != nil {
			return result, err
		}
	}
	if req.Path != "" && (path.IsAbs(req.Path) || path.Clean(req.Path) != req.Path || req.Path == ".." || strings.HasPrefix(req.Path, "../")) {
		return result, fmt.Errorf("invalid native-state relative path")
	}
	uid, gid := spaceenv.Identity()
	return ExecuteForDisk(ctx, space, cfg.AccountDiskID, cfg.AccountDiskUUID, uid, gid, req)
}

// ExecuteForDisk is the boot-time storage entry point. It does not need to
// read declarations from a filesystem which has not yet been mounted.
// Disk identity comes only from trusted host bootstrap, never a box request.
func ExecuteForDisk(ctx context.Context, space, id, uuid string, uid, gid int, req Request) (Result, error) {
	var result Result
	if _, err := layout.AccountDiskDir(id); err != nil {
		return result, err
	}
	if err := layout.ValidateExistingSpaceName(space); err != nil {
		return result, err
	}
	if req.Account != "" {
		if err := layout.ValidateName("account", req.Account); err != nil {
			return result, err
		}
	}
	if req.Agent != "" {
		if err := layout.ValidateName("agent", req.Agent); err != nil {
			return result, err
		}
	}
	if req.Path != "" && (path.IsAbs(req.Path) || path.Clean(req.Path) != req.Path || req.Path == ".." || strings.HasPrefix(req.Path, "../")) {
		return result, fmt.Errorf("invalid native-state relative path")
	}
	req.ID, req.UUID = id, uuid
	req.Mount = layout.AccountDiskMount(req.ID)
	req.Root, _ = layout.AccountStateRoot()
	req.UID, req.GID = uid, gid
	body, err := json.Marshal(req)
	if err != nil {
		return result, err
	}
	if req.Action == "verify" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, accountVerificationBudget)
		defer cancel()
	}
	cmd := accountStateCommand(ctx, space, "sudo", "python3", "-c", guestScript)
	cmd.Stdin = bytes.NewReader(body)
	if req.Action == "verify" {
		result, err := runAccountVerification(ctx, cmd)
		if err != nil {
			return Result{}, fmt.Errorf("space %s native account verification: %w", space, err)
		}
		return result, nil
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return result, fmt.Errorf("space %s native account state: %w: %s", space, err, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("decode account-state response: %w", err)
	}
	return result, nil
}
