package imagejob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/containerd/platforms"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// LocalRetainBuilder selects only retention of an existing local image target.
// It grants no permission to fetch, build, publish or execute an image.
const LocalRetainBuilder = "local-retain/1"

// LocalTarget records the complete source image target descriptor. The target
// can be a manifest or index; a platform configuration digest is not a target.
type LocalTarget struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

// NewLocalRetain freezes the selected local source, target and platform. The
// backend must revalidate that source and retain its complete native content
// graph under the original admitted job before returning a usable pin.
func NewLocalRetain(reference, platform string, target LocalTarget) (Recipe, error) {
	canonical, err := CanonicalReference(reference)
	if err != nil {
		return Recipe{}, fmt.Errorf("local image source: %w", err)
	}
	canonicalPlatform, err := localRetainPlatform(platform)
	if err != nil {
		return Recipe{}, err
	}
	r := Recipe{Builder: LocalRetainBuilder, Platform: canonicalPlatform, BaseReference: canonical,
		BaseDigest: target.Digest, LocalTarget: &target}
	if err := r.ValidateLocalRetain(); err != nil {
		return Recipe{}, err
	}
	return r, nil
}

func localRetainPlatform(platform string) (string, error) {
	if !strings.Contains(platform, "/") {
		return "", errors.New("local image retention requires its explicit platform")
	}
	parsed, err := platforms.Parse(platform)
	if err != nil {
		return "", fmt.Errorf("local image platform: %w", err)
	}
	return platforms.Format(platforms.Normalize(parsed)), nil
}

// ValidateLocalRetain rejects mixed build/retain recipes instead of ignoring
// build inputs. It verifies immutable metadata only, never local availability,
// content completeness, native-platform compatibility or lease ownership.
func (r Recipe) ValidateLocalRetain() error {
	if r.Builder != LocalRetainBuilder || r.LocalTarget == nil {
		return errors.New("local image retention requires its closed recipe and target")
	}
	if len(r.Dockerfile) != 0 || len(r.Images) != 0 || len(r.Files) != 0 || len(r.FileModes) != 0 || len(r.Args) != 0 || r.Force || r.NoCache {
		return errors.New("local image retention does not accept build or refresh inputs")
	}
	canonical, err := CanonicalReference(r.BaseReference)
	if err != nil || canonical != r.BaseReference {
		return errors.New("local image retention requires its canonical source reference")
	}
	platform, err := localRetainPlatform(r.Platform)
	if err != nil {
		return err
	}
	if platform != r.Platform {
		return errors.New("local image retention requires its canonical platform")
	}
	target := r.LocalTarget
	if err := digest.Digest(target.Digest).Validate(); err != nil {
		return fmt.Errorf("local image target must be immutable: %w", err)
	}
	if r.BaseDigest != target.Digest {
		return errors.New("local image base digest differs from its target")
	}
	if target.Size <= 0 {
		return errors.New("local image target requires its positive descriptor size")
	}
	switch target.MediaType {
	case ocispec.MediaTypeImageManifest, ocispec.MediaTypeImageIndex,
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json":
	default:
		return errors.New("local image target must be an image manifest or index")
	}
	return nil
}

func (r Recipe) localRetainIdentity() (string, error) {
	if err := r.ValidateLocalRetain(); err != nil {
		return "", err
	}
	h := sha256.New()
	for _, field := range []string{"yoloctl local image retain v1", r.Builder, r.BaseReference,
		r.Platform, r.LocalTarget.Digest, strconv.FormatInt(r.LocalTarget.Size, 10), r.LocalTarget.MediaType} {
		hashField(h, []byte(field))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
