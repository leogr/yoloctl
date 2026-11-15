package features

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

func validateCacheDirectories(dirs []string) error {
	for _, dir := range dirs {
		if dir == "." || !fs.ValidPath(dir) || strings.ContainsRune(dir, 0) {
			return fmt.Errorf("cache directory %q must be a relative path without dot or parent components", dir)
		}
	}
	return nil
}

// RequiredCacheDirectories returns the selected catalog features' cache setup
// paths. It does not prepare storage or add a mount or environment variable.
func RequiredCacheDirectories(names []string) ([]string, error) {
	var dirs []string
	for _, name := range names {
		feature, err := Lookup(name)
		if err != nil {
			return nil, err
		}
		if err := validateCacheDirectories(feature.CacheDirectories); err != nil {
			return nil, fmt.Errorf("feature %s: %w", name, err)
		}
		dirs = append(dirs, feature.CacheDirectories...)
	}
	slices.Sort(dirs)
	return slices.Compact(dirs), nil
}
