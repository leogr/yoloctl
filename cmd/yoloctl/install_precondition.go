package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/installmigration"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/spf13/cobra"
)

const installPreconditionFlag = "install-precondition"
const installReceiptFlag = "install-result-fd"

type installSkipped struct{ reason error }

func (e *installSkipped) Error() string          { return fmt.Sprintf("state changed, skipped: %v", e.reason) }
func (e *installSkipped) Unwrap() error          { return e.reason }
func (*installSkipped) passthroughExitCode() int { return installmigration.SkipStatus }
func skipped(reason error) error                 { return &installSkipped{reason: reason} }

type installConsentKey struct{}

func hasInstallPrecondition(cmd *cobra.Command) bool {
	flag := cmd.Flag(installPreconditionFlag)
	return flag != nil && flag.Value.String() != ""
}

func installBootForbidden(ctx context.Context) bool {
	value, _ := ctx.Value(installConsentKey{}).(bool)
	return value
}

func consentSnapshot(boot *spaceconfig.Bootstrap, state string) (installmigration.Snapshot, error) {
	if boot.Initializing || boot.Import != nil || boot.ImageUpdate != nil {
		return installmigration.Snapshot{}, errors.New("space requires initialization/import/image recovery")
	}
	raw, err := json.Marshal(boot)
	if err != nil {
		return installmigration.Snapshot{}, err
	}
	s := installmigration.Snapshot{Space: boot.Name, Bundle: boot.Bundle, State: state, DiskID: boot.DiskID, DiskUUID: boot.DiskUUID, Generation: boot.Generation, Bootstrap: fmt.Sprintf("%x", sha256.Sum256(raw))}
	return s, s.Validate()
}

// Tests substitute observations, never a production environment escape.
var observeInstallConsent = readInstallConsent

func readInstallConsent(ctx context.Context, space string) (installmigration.Snapshot, error) {
	boot, err := spaceconfig.LoadBootstrap(space)
	if err != nil {
		return installmigration.Snapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inst, found, err := findSpaceInstance(ctx, space)
	if err != nil {
		return installmigration.Snapshot{}, err
	}
	if !found {
		return installmigration.Snapshot{}, errors.New("VM is missing")
	}
	return consentSnapshot(boot, inst.Status)
}

// Checked immediately after acquiring the existing host lifecycle lock. A
// result pipe is private to the installer and never inherited by subprocesses.
func checkInstallPrecondition(cmd *cobra.Command, space, action string) error {
	value, _ := cmd.Flags().GetString(installPreconditionFlag)
	fd, _ := cmd.Flags().GetInt(installReceiptFlag)
	if value == "" && fd == 0 {
		return nil
	}
	if value == "" || fd != 3 {
		return skipped(errors.New("consent precondition requires its private result pipe"))
	}
	if err := validateInstallResultPipe(fd); err != nil {
		return skipped(err)
	}

	expected, err := installmigration.Decode(value)
	if err != nil || expected.Space != space {
		return skipped(errors.New("invalid consent snapshot"))
	}
	current, err := observeInstallConsent(cmd.Context(), space)
	if err != nil {
		if ctxErr := cmd.Context().Err(); ctxErr != nil {
			return ctxErr // interrupted, not skipped
		}
		return skipped(err)
	}
	if current != expected {
		return skipped(installmigration.ErrChanged)
	}
	if action == "start" && expected.State == "Running" {
		cmd.SetContext(context.WithValue(cmd.Context(), installConsentKey{}, true))
	}
	return nil
}

func writeInstallReceipt(cmd *cobra.Command, space string) error {
	value, _ := cmd.Flags().GetString(installPreconditionFlag)
	if value == "" {
		return nil
	}
	before, err := installmigration.Decode(value)
	if err != nil {
		return err
	}
	after, err := observeInstallConsent(cmd.Context(), space)
	if err != nil {
		return fmt.Errorf("operation completed but its consent result is unavailable: %w", err)
	}
	if before.DiskID != after.DiskID || before.DiskUUID != after.DiskUUID || after.State != "Running" {
		return errors.New("operation result identity/state changed")
	}
	f := os.NewFile(3, "installer-result")
	if f == nil {
		return errors.New("installer result pipe missing")
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(installmigration.Receipt{Before: before, After: after})
}

var validateInstallResultPipe = checkInstallResultPipe

func checkInstallResultPipe(fd int) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		return errors.New("installer result descriptor is not a pipe")
	}
	syscall.CloseOnExec(fd)
	return nil
}
