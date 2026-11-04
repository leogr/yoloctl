package controller

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/safepath"
)

// CatalogSnapshot captures only regular YAML catalog files from the read-only
// mounts. Rechecking the inventory rejects host edits made during the copy.
// Later host edits affect the next operation, never this worker's resolution.
func CatalogSnapshot(source, parent string) (string, string, func(), error) {
	read := func() (map[string][]byte, error) {
		docs := map[string][]byte{}
		for _, kind := range []string{"agents", "features", "capabilities"} {
			entries, err := os.ReadDir(filepath.Join(source, kind))
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
					continue
				}
				rel := filepath.Join(kind, entry.Name())
				raw, err := safepath.ReadFile(source, rel, MaxDocument)
				if err != nil {
					return nil, fmt.Errorf("catalog %s: %w", rel, err)
				}
				docs[rel] = raw
			}
		}
		return docs, nil
	}
	for range 3 {
		docs, err := read()
		if err != nil {
			return "", "", nil, err
		}
		check, err := read()
		if err != nil {
			return "", "", nil, err
		}
		digest := documentDigest(docs)
		if digest != documentDigest(check) {
			continue
		}
		dir, err := os.MkdirTemp(parent, "catalogs-")
		if err != nil {
			return "", "", nil, err
		}
		cleanup := func() { _ = os.RemoveAll(dir) }
		for _, kind := range []string{"agents", "features", "capabilities"} {
			if err := os.Mkdir(filepath.Join(dir, kind), 0o700); err != nil {
				cleanup()
				return "", "", nil, err
			}
		}
		for rel, raw := range docs {
			if err := safepath.WriteFile(dir, rel, raw, 0o600); err != nil {
				cleanup()
				return "", "", nil, err
			}
		}
		return dir, digest, cleanup, nil
	}
	return "", "", nil, errors.New("catalogs changed repeatedly while taking an operation snapshot; retry")
}
