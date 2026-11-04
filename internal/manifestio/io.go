// Package manifestio routes declaration I/O through the verified native store
// in controller workers. Host-side legacy readers remain available for migration.
package manifestio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

const MaxSize = 1 << 20

func relative(path string) (string, error) {
	rel, err := filepath.Rel(spaceenv.Current().ConfigDir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errors.New("declaration path escapes the native configuration store")
	}
	return rel, nil
}

func Read(path string) ([]byte, error) {
	if c := spaceenv.Current(); c != nil {
		rel, err := relative(path)
		if err != nil {
			return nil, err
		}
		return safepath.ReadFile(c.ConfigDir, rel, MaxSize)
	}
	return os.ReadFile(path)
}

func Write(path string, data []byte, mode os.FileMode) error {
	if len(data) > MaxSize {
		return errors.New("declaration exceeds size limit")
	}
	if c := spaceenv.Current(); c != nil {
		rel, err := relative(path)
		if err != nil {
			return err
		}
		return safepath.WriteFile(c.ConfigDir, rel, data, 0o600)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, data, mode)
}

func Remove(path string) error {
	if c := spaceenv.Current(); c != nil {
		rel, err := relative(path)
		if err != nil {
			return err
		}
		return safepath.RemoveFile(c.ConfigDir, rel)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return atomicfile.SyncDir(filepath.Dir(path))
}
