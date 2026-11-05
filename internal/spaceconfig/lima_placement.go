package spaceconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/strictyaml"
)

// ValidateLimaPlacement checks existing host grants before placing VM/disk
// storage. Mount admission protects future grants; this check also covers
// grants saved before macOS's default Lima home changed. It is read-only:
// stopped spaces and read-only grants still count, and no VM is remounted.
func ValidateLimaPlacement(home string) error {
	home, err := layout.ResolveLimaHome(home)
	if err != nil {
		return err
	}
	canonical, err := canonicalPlacementPath(home)
	if err != nil {
		return fmt.Errorf("resolve Lima storage %q: %w", home, err)
	}
	names, err := placementSpaceNames()
	if err != nil {
		return fmt.Errorf("inspect existing space grants before placing Lima storage: %w", err)
	}
	for _, name := range names {
		grants, err := placementGrants(name)
		if errors.Is(err, os.ErrNotExist) {
			continue // An absent manifest is an incomplete/disappeared bundle.
		}
		if err != nil {
			return fmt.Errorf("inspect host grants of space %q before placing Lima storage: %w", name, err)
		}
		for _, grant := range grants {
			source, err := canonicalPlacementPath(grant.Source)
			if err != nil {
				return fmt.Errorf("resolve host grant %q of space %q: %w", grant.Source, name, err)
			}
			if placementPathsOverlap(canonical, source) {
				return fmt.Errorf("cannot place Lima storage %q: overlaps host grant %q of space %q; use a non-overlapping YOLOCTL_HOME for the new installation", home, grant.Source, name)
			}
		}
	}
	return nil
}

// Placement needs an authoritative inventory, unlike display-oriented List.
// Include every valid bundle directory and let the no-follow manifest reader
// establish presence. Never hide permission/metadata errors behind Stat.
func placementSpaceNames() ([]string, error) {
	dir, err := layout.SpacesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read space bundle directory %q: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if layout.ValidateExistingSpaceName(name) != nil {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect space bundle %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("space bundle %q is a symbolic link; cannot verify its grants", name)
		}
		if info.IsDir() {
			names = append(names, name)
		}
	}
	return names, nil
}

// Historical schema-2 records can still describe granted shares. Read their
// grants for exclusion only; never use this inventory as boot/declaration data.
// Both formats are bounded, no-follow reads with strict identity validation.
func placementGrants(name string) ([]Mount, error) {
	dir, err := layout.SpaceBundleDir(name)
	if err != nil {
		return nil, err
	}
	raw, err := safepath.ReadFile(dir, "space.yaml", 1<<20)
	if err != nil {
		return nil, err
	}
	boot, err := ParseBootstrap(raw)
	if err == nil {
		if boot.Name != name {
			return nil, errors.New("bootstrap space identity mismatch")
		}
		return boot.Mounts, nil
	}
	if !errors.Is(err, ErrLegacySchema) {
		return nil, err
	}
	var legacy Config
	if err := strictyaml.Decode(raw, &legacy); err != nil {
		return nil, err
	}
	if legacy.Name != name {
		return nil, errors.New("historical space identity mismatch")
	}
	if err := legacy.Validate(); err != nil {
		return nil, err
	}
	return legacy.Mounts, nil
}

// Resolve existing ancestors without creating the missing storage suffix.
// An existing dangling symlink is an error, not a directory to allocate through.
func canonicalPlacementPath(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("host path must be absolute and clean")
	}
	var suffix []string
	for current := path; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if filepath.Dir(current) == current {
			return "", fmt.Errorf("no existing ancestor of %q", path)
		}
		suffix = append(suffix, filepath.Base(current))
	}
}

func placementPathsOverlap(a, b string) bool {
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
		// Existing directory identities also cover case aliases on macOS.
		info, err := os.Stat(parent)
		if err != nil {
			return false
		}
		for current := child; ; current = filepath.Dir(current) {
			if candidate, err := os.Stat(current); err == nil && os.SameFile(info, candidate) {
				return true
			}
			if filepath.Dir(current) == current {
				return false
			}
		}
	}
	return contains(a, b) || contains(b, a)
}
