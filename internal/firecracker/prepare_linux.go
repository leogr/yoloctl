//go:build linux && amd64

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/rotatelog"
	"golang.org/x/sys/unix"
)

// A stable lock serializes installers across spaces/processes. Failed or
// cancelled builders cannot publish a partial default image.
func imageLock(ctx context.Context, root string) (func(), error) {
	fd, err := unix.Open(filepath.Join(root, ".prepare.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	closeFD := func() { _ = unix.Close(fd) }
	waiting := time.NewTimer(time.Second)
	defer waiting.Stop()
	for {
		if err := ctx.Err(); err != nil {
			closeFD()
			return nil, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return closeFD, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			closeFD()
			return nil, err
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		case <-waiting.C:
			slog.Info("Waiting for another local VM image preparation; Ctrl-C cancels.", "actor", "vm", "executor", "firecracker")
		}
	}
}

func prepareLocalImage(ctx context.Context, stage string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	init, err := guestbinary.ResolveInit(self)
	if err != nil {
		return "", err
	}
	cache, err := layout.VMImageBuildDir("firecracker")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return "", err
	}
	// Config roots can share an XDG cache. Protect reusable downloads/kernels
	// there too, always after the immutable image store lock.
	unlock, err := imageLock(ctx, cache)
	if err != nil {
		return "", err
	}
	defer unlock()
	log, err := os.CreateTemp(cache, "prepare-*.log")
	if err != nil {
		return "", err
	}
	logName := log.Name()
	if err := log.Close(); err != nil {
		return "", err
	}
	bounded, err := rotatelog.Open(logName, 1<<20)
	if err != nil {
		return "", err
	}
	defer func() { _ = bounded.Close() }()
	fileLog := logging.NewFileLogger(bounded)
	defer func() { _ = fileLog.Close() }()
	logMessage := func(message string) {
		slog.Info(message, "actor", "vm", "executor", "firecracker")
		fileLog.Slog().Info(message, "actor", "vm", "executor", "firecracker")
	}
	out := filepath.Join(stage, "image")
	slog.Info("Preparing a Firecracker image locally on first use. This can take several minutes; later spaces reuse it.\nBuild log: "+logName, "actor", "vm", "executor", "firecracker")
	if err := assembleLocalImage(ctx, out, cache, init, logMessage); err != nil {
		return "", fmt.Errorf("prepare local Firecracker image: %w (log: %s)", err, logName)
	}
	return out, nil
}
