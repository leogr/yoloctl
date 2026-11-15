package agentbox

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/leogr/yoloctl/internal/boxcache"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

var ensureCacheDirectory = boxcache.Ensure
var removeCacheDirectory = boxcache.Remove

// ValidateCacheDeclaration checks only cache-target conflicts, without I/O.
// Existing manifests stay readable for explicit stop/destroy recovery.
func ValidateCacheDeclaration(c *Config) error {
	if c == nil || c.Privileged {
		return nil
	}
	for _, m := range c.Mounts {
		if chatTargetsOverlap(path.Clean(m.Target), boxcache.Target) {
			return fmt.Errorf("mount target %s overlaps the box cache %s; change or remove that mount before creating or recreating the box", m.Target, boxcache.Target)
		}
	}
	return nil
}

func addCacheRunOptions(c *Config, opts *container.RunOptions) error {
	if c.Privileged || !spaceenv.Native() {
		return nil
	}
	if err := spaceenv.RequireSpace(c.Space); err != nil {
		return err
	}
	native := spaceenv.Current()
	return addCacheOptions(c, opts, native.UID, native.GID)
}

func addCacheOptions(c *Config, opts *container.RunOptions, uid, gid int) error {
	if c.Privileged {
		return nil
	}
	if err := ValidateCacheDeclaration(c); err != nil {
		return err
	}
	for _, m := range opts.Mounts {
		if chatTargetsOverlap(path.Clean(m.Destination), boxcache.Target) {
			return errors.New("runtime mount overlaps the box cache " + boxcache.Target)
		}
	}
	source, err := boxcache.Path(c.Name)
	if err != nil {
		return err
	}
	dirs, err := requiredCacheDirectories(c)
	if err != nil {
		return err
	}
	if err := ensureCacheDirectory(c.Name, uid, gid); err != nil {
		return fmt.Errorf("prepare box cache: %w", err)
	}
	if len(dirs) > 0 {
		if err := prepareFeatureCacheDirectories(context.Background(), c.Name, uid, gid, dirs); err != nil {
			return err
		}
	}
	opts.Mounts = append(opts.Mounts, container.Mount{Source: source, Destination: boxcache.Target})
	return nil
}

// CacheBound recognises only this box's read-write controller-derived leaf.
// A legacy container without one is left unchanged and does not need recreate.
func CacheBound(name string, details *container.Details) (bool, error) {
	source, err := boxcache.Path(name)
	if err != nil {
		return false, err
	}
	found := false
	for _, m := range details.Mounts {
		if m.Destination != boxcache.Target {
			continue
		}
		if found || m.Source != source || m.ReadOnly {
			return false, errors.New("box cache bind differs from its controller-owned leaf")
		}
		found = true
	}
	return found, nil
}

// RemoveCacheRuntime runs only after authoritative container removal. It is
// separate from normal stop/restart/recreate, which preserve this directory.
func RemoveCacheRuntime(name string) error {
	if _, err := boxcache.Path(name); err != nil {
		return err
	}
	native := spaceenv.Current()
	if native == nil {
		// Non-native legacy callers never provision these controller roots.
		return nil
	}
	return removeCacheDirectory(name, native.UID, native.GID)
}

// PrepareCacheRuntime verifies retained cache storage before starting a bound
// container. A pre-D1 container is not changed or retrofitted.
func PrepareCacheRuntime(c *Config, details *container.Details) error {
	if c.Privileged {
		return nil
	}
	bound, err := CacheBound(c.Name, details)
	if err != nil || !bound {
		return err
	}
	native := spaceenv.Current()
	if native == nil {
		return errors.New("box cache preparation requires the native space controller")
	}
	if err := spaceenv.RequireSpace(c.Space); err != nil {
		return err
	}
	dirs, err := requiredCacheDirectories(c)
	if err != nil {
		return err
	}
	if err := ensureCacheDirectory(c.Name, native.UID, native.GID); err != nil {
		return err
	}
	if len(dirs) > 0 {
		return prepareFeatureCacheDirectories(context.Background(), c.Name, native.UID, native.GID, dirs)
	}
	return nil
}
