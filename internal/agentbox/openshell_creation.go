package agentbox

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/openshell"
)

type nativeCreationRelease struct {
	release openshell.QualifiedRelease
	rego    []byte
}

// WithNativeCreationRelease binds the controller's independently reviewed
// release selection. It is not a declaration/config switch or admission: the
// actual Linux adapters still require current native storage and effect gates.
// An invalid selection fails closed; it never falls back to legacy creation.
// Existing containers keep their inspected, recorded generation's lifecycle.
func (a LiveApplier) WithNativeCreationRelease(release openshell.QualifiedRelease, rego []byte) LiveApplier {
	a.nativeCreation = &nativeCreationRelease{release: release, rego: bytes.Clone(rego)}
	return a
}

// PreflightRecreate qualifies the exact replacement before a managed caller
// ends its holder or withdraws forwards. The immutable image is retained by
// the enclosing controller image-preparation pin across this check and Create.
// No container, endpoint, incarnation or per-box generation is created here.
func (a LiveApplier) PreflightRecreate(ctx context.Context, c *Config, policy []byte) error {
	if c == nil {
		return errors.New("replacement qualification requires its declaration")
	}
	if c.Privileged || a.nativeCreation == nil {
		return nil
	}
	_, err := a.PreflightNativeCreate(ctx, c, policy)
	return err
}

// PreflightNativeCreate is also used before ordinary common preparation
// (account directories, declaration normalization and workdir preparation).
// Production callers must bind the compiled release before selecting this
// route. The returned image is the retained OCI target, never a mutable tag.
func (a LiveApplier) PreflightNativeCreate(ctx context.Context, c *Config, policy []byte) (string, error) {
	if c == nil || c.Privileged || a.nativeCreation == nil || a.Client == nil {
		return "", errors.New("native creation qualification requires an ordinary box and selected release")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.Workdir.MountSource() == "" {
		return "", errors.New("native creation requires its actual workdir")
	}
	if err := a.nativeCreationPrerequisite(ctx); err != nil {
		return "", err
	}
	candidates, err := RuntimeFeatures(c)
	if err != nil {
		return "", err
	}
	plan, err := c.FeaturePlan()
	if err != nil {
		return "", err
	}
	image, err := agentboximage.RequirePreparedImage(ctx, a.Client, c.Image, candidates, agentboximage.Options{FeaturePlan: plan})
	if err != nil {
		return "", fmt.Errorf("ensure native runtime image for %s: %w", c.Name, err)
	}
	if err := a.preflightQualifiedNativeCreation(ctx, c, image, policy); err != nil {
		return "", err
	}
	return image, nil
}

func nativeCreationImage(image string) error {
	digest := strings.TrimPrefix(image, "sha256:")
	_, err := hex.DecodeString(digest)
	if !strings.HasPrefix(image, "sha256:") || len(digest) != 64 || strings.ToLower(digest) != digest || err != nil {
		return errors.New("native creation requires the retained prepared OCI target digest")
	}
	return nil
}
