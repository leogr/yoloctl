package agentbox

import (
	"context"
	"fmt"
	"time"

	"github.com/leogr/yoloctl/internal/ocifeature"
)

// ResolveFeatures is the declaration mutation boundary. Existing locks are
// retained; reads never contact registries. Remove feature_locks in box edit
// to explicitly resolve moving tags again.
func (c *Config) ResolveFeatures(ctx context.Context) error {
	hasOCI := len(c.FeatureOptions) > 0 || len(c.FeatureLocks) > 0
	for _, name := range c.Features {
		hasOCI = hasOCI || ocifeature.IsReference(name)
	}
	names, err := EffectiveFeaturesStrict(c)
	if err != nil {
		// Direct Save historically permits unavailable profiles. CLI writes
		// separately require a resolvable policy before persistence.
		if !hasOCI {
			return nil
		}
		return err
	}
	for _, name := range names {
		hasOCI = hasOCI || ocifeature.IsReference(name)
	}
	if !hasOCI {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	plan, err := ocifeature.Resolve(ctx, ocifeature.NewRegistry(), names, c.FeatureOptions, c.FeatureLocks)
	if err != nil {
		return fmt.Errorf("box %s features: %w", c.Name, err)
	}
	c.FeatureLocks = plan.Locks
	return nil
}
func (c *Config) FeaturePlan() (ocifeature.Plan, error) {
	names, err := EffectiveFeaturesStrict(c)
	if err != nil {
		return ocifeature.Plan{}, err
	}
	return ocifeature.Resolve(context.Background(), nil, names, c.FeatureOptions, c.FeatureLocks)
}
