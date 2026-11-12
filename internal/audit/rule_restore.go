package audit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

// Restore publishes only rule source/metadata during explicit native archive
// import. It does not install tools, retain absolute configurations or enable
// capture. Explicit enable prepares/validates a new current configuration.
func (store RuleStore) Restore(ctx context.Context, set RuleSet, read func(RuleFile) ([]byte, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := set.check(); err != nil {
		return err
	}
	dest, err := layout.AuditRulesGeneration(store.Root, set.Generation)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		current, err := store.Read(set.Generation)
		if err != nil {
			return err
		}
		if current.Fingerprint != set.Fingerprint {
			return errors.New("different audit rule generation already occupies import destination")
		}
		return nil // A pending import may already have moved the identical bytes.
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := safepath.PrivateDirectory(store.Root, "rules", os.Geteuid(), os.Getegid()); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(layout.AuditRulesDir(store.Root), ".restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	for _, file := range set.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := read(file)
		if err != nil {
			return err
		}
		if len(raw) > MaxRuleFile || Digest(raw) != file.SHA256 {
			return errors.New("imported audit rule file size or checksum mismatch")
		}
		if err := safepath.WriteFile(stage, ruleFilePath(file), raw, 0600); err != nil {
			return err
		}
	}
	if err := safepath.WriteFile(stage, "selection.yaml", set.Selection, 0600); err != nil {
		return err
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if err := safepath.WriteFile(stage, "manifest.json", raw, 0600); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	// Namespace publication follows fsync of all staged file/parent descriptors.
	return atomicfile.SyncDir(filepath.Dir(dest))
}
