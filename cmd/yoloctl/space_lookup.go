package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

type absentSpaceError struct{ name string }

func (e absentSpaceError) Error() string { return fmt.Sprintf("space %q does not exist", e.name) }
func (e absentSpaceError) Unwrap() error { return fs.ErrNotExist }

func loadRequiredSpaceBootstrap(name string) (*spaceconfig.Bootstrap, error) {
	boot, err := spaceconfig.LoadBootstrap(name)
	return boot, classifySpaceBootstrapError(name, err)
}

// A retained bundle with a missing bootstrap needs recovery. Keep the raw
// bootstrap loader available to discovery and creation paths.
func classifySpaceBootstrapError(name string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir, pathErr := layout.SpaceBundleDir(name)
	if pathErr != nil {
		return pathErr
	}
	_, statErr := os.Lstat(dir)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		return asUserError(absentSpaceError{name: name})
	case statErr != nil:
		return statErr
	default:
		return fmt.Errorf("space %q has no declaration; restore its bundle: %w", name, err)
	}
}

// SSH and VM logs can inspect a retained VM without a bootstrap. An exact
// local metadata check on the missing-name path keeps those recovery commands
// available even when a hypervisor CLI cannot run.
func requireSpaceRuntimeTarget(name string) error {
	_, err := spaceconfig.LoadBootstrap(name)
	if err == nil || (!errors.Is(err, fs.ErrNotExist) && !errors.Is(err, spaceconfig.ErrLegacySchema)) {
		return err
	}
	// Without a backend declaration the runtime commands would select Lima.
	// Refuse to use that fallback when Firecracker metadata survives.
	firecracker, err := layout.FirecrackerInstanceDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(firecracker); err == nil {
		return asUserError(fmt.Errorf("space %q has conflicting firecracker VM metadata; restore its bootstrap before managing it", name))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	bundle, err := layout.SpaceBundleDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(bundle); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	instance, err := layout.LimaInstanceDir("", name)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(instance); !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	// Another declaration may still name the home of an orphaned Lima VM.
	// Keep that recovery path without requiring limactl to prove absence.
	homes, err := spaceconfig.LimaHomes()
	if err != nil {
		return err
	}
	for _, home := range homes {
		dir, err := layout.LimaInstanceDir(home, name)
		if err != nil {
			return err
		}
		if dir == instance {
			continue
		}
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return asUserError(absentSpaceError{name: name})
}
