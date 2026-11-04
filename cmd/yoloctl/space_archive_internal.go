package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spacearchive"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
	"golang.org/x/sys/unix"
)

func openImportState(dir string) (*os.File, error) {
	fd, err := unix.Open(filepath.Join(dir, "import-state.tar"), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "import-state.tar")
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("pending import must be a regular file")
	}
	return f, nil
}

func writeControllerContext(c spaceenv.Context) error {
	if err := c.Validate(); err != nil {
		return err
	}
	identity, err := controller.ExecutableIdentity()
	if err != nil || identity != c.Bundle {
		return errors.New("bootstrap and installed controller builds differ")
	}
	if err := os.MkdirAll("/etc/yoloctl", 0755); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return safepath.WriteFile("/etc", "yoloctl/controller.json", raw, 0600)
}

func restoreNativeSpace(ctx context.Context, input io.Reader) error {
	reader := bufio.NewReaderSize(input, 1<<20)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return errors.New("invalid import header")
	}
	var req restoreRequest
	if err := decodeBoundedJSON(bytes.NewReader(line), &req, 1<<20); err != nil {
		return err
	}
	if err := writeControllerContext(req.Context); err != nil {
		return err
	}
	if err := controller.PrepareRuntimeForLifecycle(ctx); err != nil {
		return err
	}
	c := spaceenv.Current()
	release, err := spacelock.Acquire(ctx, c.Space, true)
	if err != nil {
		return err
	}
	defer release()
	boot := &spaceconfig.Bootstrap{Name: c.Space, DiskID: c.DiskID, DiskUUID: c.DiskUUID, UID: c.UID, GID: c.GID}
	return spacearchive.RestoreState(ctx, reader, c.DiskMount, boot, req.State.SHA256, req.State.Size)
}

func exportNativeSpace(ctx context.Context, output io.Writer) error {
	c := spaceenv.Current()
	release, err := spacelock.Acquire(ctx, c.Space, true)
	if err != nil {
		return err
	}
	defer release()
	boxes, err := resolveTeardownBoxes(ctx, c.Space)
	if err != nil {
		return err
	}
	for _, box := range boxes {
		if box.Container != nil && boxstate.ContainerRunning(box.Container.Status) {
			return fmt.Errorf("box %s is running; stop boxes or use space export --stop", box.Name)
		}
	}
	if err := controller.VerifyStoreDirectories(c.DiskMount); err != nil {
		return err
	}
	boot := &spaceconfig.Bootstrap{Name: c.Space, DiskID: c.DiskID, DiskUUID: c.DiskUUID, UID: c.UID, GID: c.GID}
	for _, m := range c.Mounts {
		boot.Mounts = append(boot.Mounts, spaceconfig.Mount{Source: m.Source, Writable: m.Writable})
	}
	return spacearchive.WriteState(ctx, output, c.DiskMount, boot)
}
