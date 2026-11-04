//go:build linux && amd64

package firecracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/layout"
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
			_, _ = fmt.Fprintln(os.Stderr, "Waiting for another local VM image preparation; Ctrl-C cancels.")
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
	defer func() { _ = log.Close() }()
	out := filepath.Join(stage, "image")
	_, _ = fmt.Fprintf(os.Stderr, "Preparing a Firecracker image locally on first use. This can take several minutes; later spaces reuse it.\nBuild log: %s\n", log.Name())
	if err := assembleLocalImage(ctx, out, cache, init, io.MultiWriter(os.Stderr, log)); err != nil {
		return "", fmt.Errorf("prepare local Firecracker image: %w (log: %s)", err, log.Name())
	}
	return out, nil
}
