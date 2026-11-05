package layout

import (
	"errors"
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
	return filepath.Join(c.DiskMount, "audit"), nil
}

func AuditRunDir() string { return filepath.Join(spaceenv.RunRoot, "audit") }

func AuditCacheDir() (string, error) {
	root, err := CacheRoot()
	return filepath.Join(root, "audit"), err
}
