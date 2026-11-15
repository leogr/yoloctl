package agentbox

import (
	"context"
	"errors"

	"github.com/leogr/yoloctl/internal/boxcache"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

var prepareFeatureCacheDirectories = boxcache.PrepareDirectories

func requiredCacheDirectories(c *Config) ([]string, error) {
	if c.Privileged {
		return nil, nil
	}
	names, err := EffectiveFeaturesStrict(c)
	if err != nil {
		return nil, err
	}
	return features.RequiredCacheDirectories(names)
}

// ClearCacheRuntime resolves the feature paths before any removal, then restores
// those directories after clearing. No account, installed binary or workdir moves.
func ClearCacheRuntime(ctx context.Context, c *Config) (uint64, error) {
	dirs, err := requiredCacheDirectories(c)
	if err != nil {
		return 0, err
	}
	native := spaceenv.Current()
	if native == nil {
		return 0, errors.New("box cache clear requires the native space controller")
	}
	if err := spaceenv.RequireSpace(c.Space); err != nil {
		return 0, err
	}
	return boxcache.ClearWithDirectories(ctx, c.Name, native.UID, native.GID, dirs)
}
