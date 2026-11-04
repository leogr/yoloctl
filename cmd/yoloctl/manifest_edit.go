package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spacelock"
)

type editSnapshot struct {
	path string
	data []byte
	info os.FileInfo
}

func snapshotEditFile(path string) (editSnapshot, error) {
	s := editSnapshot{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.data = data
	s.info, err = os.Stat(path)
	return s, err
}
func (s editSnapshot) unchanged() bool {
	current, err := snapshotEditFile(s.path)
	if err != nil {
		return false
	}
	if s.info == nil || current.info == nil {
		return s.info == nil && current.info == nil
	}
	return os.SameFile(s.info, current.info) && bytes.Equal(s.data, current.data)
}

// Editors own no locks. Both phases acquire space before entity, and the second
// checks the exact files captured in the first (including replacement of a
// space or identity with identical text). Reconcile remains inside the guard.
func lockManifestEdit(ctx context.Context, notice io.Writer, space string, entity func() (func(), error)) (func(), error) {
	unlockSpace, err := spacelock.AcquireWithWait(ctx, space, true, func() {
		_, _ = fmt.Fprintf(notice, "Waiting for another operation in space %s; Ctrl-C cancels.\n", space)
	})
	if err != nil {
		return nil, err
	}
	unlockEntity, err := entity()
	if err != nil {
		unlockSpace()
		return nil, err
	}
	return func() { unlockEntity(); unlockSpace() }, nil
}
func snapshotEditSpace(space string) (editSnapshot, error) {
	path, err := layout.SpaceConfigPath(space)
	if err != nil {
		return editSnapshot{}, err
	}
	return snapshotEditFile(path)
}
