package agentbox

import (
	"fmt"

	"github.com/distribution/reference"
	"github.com/leogr/yoloctl/internal/agentboximage"
)

// ValidateImage rejects invalid declared bases before persistence or image
// resolution. Loading stays permissive so existing bad manifests can be fixed.
func (c *Config) ValidateImage() error {
	if _, err := reference.ParseAnyReference(c.Image); err != nil {
		return fmt.Errorf("box %s: invalid image %q: %w", c.Name, c.Image, err)
	}
	return nil
}

// ResolvedImage returns the runtime image ref the box should actually run: the
// base c.Image when the composition installs nothing, else a content-addressed
// feature-composed ref (yoloctl/agentbox-composed:<hash>). `composed` reports
// which case applies.
//
// Offline and deterministic for the space's UID/GID and catalogs. It is the
// desired-side input to image-drift comparison, and agrees by construction with
// EnsureComposedImage (both go through agentboximage.ComposedImageRef).
func ResolvedImage(c *Config) (ref string, composed bool, err error) {
	if err := c.ValidateImage(); err != nil {
		return "", false, err
	}
	candidates, err := RuntimeFeatures(c)
	if err != nil {
		return "", false, err
	}
	plan, err := c.FeaturePlan()
	if err != nil {
		return "", false, err
	}
	ref, _, hasInstall, err := agentboximage.ComposedImageRef(c.Image, candidates, plan)
	if err != nil {
		return "", false, err
	}
	if !hasInstall {
		return c.Image, false, nil
	}
	return ref, true, nil
}
