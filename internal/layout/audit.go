package layout

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/spaceenv"
)

// AuditDir is private VM infrastructure on the verified native disk. It is
// separate from account leaves and is never part of a box's bind set.
func AuditDir() (string, error) {
	c := spaceenv.Current()
	if c == nil {
		return "", errors.New("audit storage requires a native controller")
	}
	return AuditDiskDir(c.DiskMount), nil
}

// AuditDiskDir also serves explicit offline archive operations on a verified disk.
func AuditDiskDir(disk string) string { return filepath.Join(disk, "audit") }

func AuditRunDir() string { return filepath.Join(spaceenv.RunRoot, "audit") }

// AuditRulesDir contains immutable controller-owned rule generations, never
// workload state. root is the verified native AuditDir (or an isolated fixture).
func AuditRulesDir(root string) string { return filepath.Join(root, "rules") }

func AuditRulesGeneration(root, generation string) (string, error) {
	raw, err := hex.DecodeString(generation)
	if err != nil || len(raw) != sha256.Size || fmt.Sprintf("%x", raw) != generation {
		return "", errors.New("invalid audit rule generation")
	}
	return filepath.Join(AuditRulesDir(root), generation), nil
}

func AuditCacheDir() (string, error) {
	root, err := CacheRoot()
	return filepath.Join(root, "audit"), err
}

// AuditMetadataPath stays stable across compatible controller updates, so a
// running collector follows fresh observations without another service restart.
func AuditMetadataPath() string { return filepath.Join(AuditRunDir(), "metadata.tsv") }
func AuditMetadataGeneration(generation string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(generation)))
}
