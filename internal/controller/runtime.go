package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// LoadRuntime installs the trusted execution context and verifies the actual
// mounted filesystem before any declaration path can be consumed.
func LoadRuntime(ctx context.Context) error {
	if err := configureRuntime(); err != nil {
		return err
	}
	return VerifyRuntimeStorage(ctx)
}

// PrepareRuntimeForLifecycle is reserved for fixed host-authorized deployment
// and archive restoration after stopping the old controller. Ordinary workers
// and recovery verifiers must use LoadRuntime, which never repairs a bind.
func PrepareRuntimeForLifecycle(ctx context.Context) error {
	if err := configureRuntime(); err != nil {
		return err
	}
	c := spaceenv.Current()
	_, err := executeRuntimeDisk(ctx, c.Space, c.DiskID, c.DiskUUID, c.UID, c.GID, accountstate.Request{Action: "prepare"})
	if err != nil {
		return err
	}
	return VerifyRuntimeStorage(ctx)
}

func configureRuntime() error {
	c, err := spaceenv.LoadTrusted()
	if err != nil {
		return err
	}
	if err := layout.ValidateName("space", c.Space); err != nil {
		return err
	}
	if c.DiskMount != layout.AccountDiskMount(c.DiskID) {
		return errors.New("controller disk mount identity mismatch")
	}
	identity, err := ExecutableIdentity()
	if err != nil {
		return err
	}
	if c.Bundle != identity {
		return errors.New("installed executable does not match the selected controller bundle")
	}
	if err := spaceenv.Configure(c); err != nil {
		return err
	}
	return nil
}

var executeRuntimeDisk = accountstate.ExecuteForDisk

// VerifyRuntimeStorage rechecks the current filesystem UUID, ownership marker
// and live account-state bind without creating directories or mounting them.
// The helper runs in a bounded child process, including at degraded retries.
func VerifyRuntimeStorage(ctx context.Context) error {
	return verifyRuntimeStorage(ctx, spaceenv.Current())
}

func verifyRuntimeStorage(ctx context.Context, c *spaceenv.Context) error {
	if c == nil {
		return errors.New("native runtime context is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := executeRuntimeDisk(ctx, c.Space, c.DiskID, c.DiskUUID, c.UID, c.GID, accountstate.Request{Action: "verify"})
	if err != nil {
		return err
	}
	if result.UUID == "" || result.UUID != c.DiskUUID {
		return errors.New("native account disk filesystem identity changed")
	}
	return nil
}

// PrivateDir opens or creates one root-owned directory on the verified disk,
// without following a substituted symlink. Used only for controller metadata.
func PrivateDir(parent, name string) (string, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return "", errors.New("invalid private directory name")
	}
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Mkdirat(fd, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return "", err
	}
	child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(child) }()
	var st unix.Stat_t
	if err := unix.Fstat(child, &st); err != nil {
		return "", err
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0o077 != 0 {
		return "", errors.New("controller directory is not private to its owner")
	}
	if err := unix.Fsync(child); err != nil {
		return "", err
	}
	if err := unix.Fsync(fd); err != nil {
		return "", &atomicfile.CommitError{Err: err}
	}
	return filepath.Join(parent, name), nil
}
