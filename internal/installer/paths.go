package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidatePaths separates writable selection entries from immutable source and
// store trees, including directory aliases and not-yet-created descendants.
// A legitimate selection symlink points into the store: resolve its parent,
// not that final symlink, because publication replaces the directory entry.
func ValidatePaths(source, store, bin string) error {
	resolved := make([]string, 3)
	for i, path := range []string{source, store, bin} {
		canonical, err := resolveInstallPath(path)
		if err != nil {
			return err
		}
		resolved[i] = canonical
	}
	for _, protected := range resolved[:2] {
		if installPathsOverlap(resolved[2], protected) {
			return fmt.Errorf("bin directory overlaps immutable source or store: %s", bin)
		}
	}
	return nil
}

// CanonicalPath resolves the existing ancestors of an absolute, clean install
// path (a home or ~/.local behind a symlink) without creating anything. The
// final component itself is kept: a store or bin directory that is a symlink
// is still refused by the no-follow traversal, never written through.
func CanonicalPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("install directory must be absolute and clean")
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}
	resolved, err := resolveInstallPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

// Resolve existing ancestors without creating missing path components. A
// dangling link is an error, rather than a missing directory to be created.
func resolveInstallPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("install directory must be absolute and clean")
	}
	var tail []string
	current := path
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
}

func installPathsOverlap(a, b string) bool {
	inside := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
		// Existing directories can have alternative spellings on case-insensitive
		// filesystems. Compare ancestor identities as well as lexical paths.
		info, err := os.Stat(parent)
		if err != nil {
			return false
		}
		for current := child; ; current = filepath.Dir(current) {
			candidate, err := os.Stat(current)
			if err == nil && os.SameFile(info, candidate) {
				return true
			}
			if filepath.Dir(current) == current {
				return false
			}
		}
	}
	return inside(a, b) || inside(b, a)
}
