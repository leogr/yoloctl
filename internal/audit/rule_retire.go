package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
)

// Retire removes only verified generated directories not referenced by keep.
// The caller holds native audit serialization, protects selected/prior/service
// references, and retires before recording the operation's completion.
func (store RuleStore) Retire(ctx context.Context, keep ...string) error {
	retained := make(map[string]bool, len(keep))
	for _, generation := range keep {
		if generation == "" {
			continue
		}
		if _, err := layout.AuditRulesGeneration(store.Root, generation); err != nil {
			return err
		}
		retained[generation] = true
	}
	entries, err := os.ReadDir(layout.AuditRulesDir(store.Root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if !validDigest(entry.Name()) || retained[entry.Name()] {
			continue
		}
		set, err := store.Read(entry.Name())
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err := store.Discard(set); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Discard is only for a caller-owned, proved-unselected generation, never an
// arbitrary path. Unknown entries or changed metadata are retained for review.
func (store RuleStore) Discard(set RuleSet) error {
	current, err := store.Read(set.Generation)
	if err != nil {
		return err
	}
	if current.Fingerprint != set.Fingerprint {
		return errors.New("audit generation changed before retirement")
	}
	set = current
	dir, err := layout.AuditRulesGeneration(store.Root, set.Generation)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	expected := make(map[string]bool, len(set.Files))
	for _, file := range set.Files[1:] {
		expected[file.SHA256+".yaml"] = true
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "manifest.json", "selection.yaml", "rules.yaml", "falco.json":
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("unexpected audit generation entry type; retained")
			}
		case "files":
			if !entry.IsDir() {
				return errors.New("unexpected audit generation directory type; retained")
			}
			files, err := os.ReadDir(filepath.Join(dir, "files"))
			if err != nil {
				return err
			}
			for _, file := range files {
				info, err := file.Info()
				if err != nil {
					return err
				}
				if !expected[file.Name()] || !info.Mode().IsRegular() {
					return errors.New("unlisted audit generation file; retained")
				}
			}
		default:
			return errors.New("unlisted audit generation entry; retained")
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return atomicfile.SyncDir(layout.AuditRulesDir(store.Root))
}
