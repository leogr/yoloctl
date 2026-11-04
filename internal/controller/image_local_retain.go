package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/leogr/yoloctl/internal/imagejob"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const localImageTimeout = 30 * time.Second
const localImageMetadataLimit = 16 << 20
const localImageGraphLimit = 16 << 10

// ResolveLocal reads only the selected system-containerd namespace. A missing
// source image retains its typed NotFound; missing content in an existing image
// is a validation error, never permission to repair it through a registry.
func (b *NativeImageBackend) ResolveLocal(ctx context.Context, ref string) (imagejob.LocalTarget, string, error) {
	if b == nil || b.Containerd == nil {
		return imagejob.LocalTarget{}, "", errors.New("local image resolution requires containerd")
	}
	name, err := canonicalImage(ref)
	if err != nil {
		return imagejob.LocalTarget{}, "", err
	}
	ctx, cancel := context.WithTimeout(imageContext(ctx), localImageTimeout)
	defer cancel()
	image, err := b.Containerd.ImageService().Get(ctx, name)
	if err != nil {
		return imagejob.LocalTarget{}, "", err
	}
	if image.Name != name {
		return imagejob.LocalTarget{}, "", errors.New("local image observation differs from its selected source")
	}
	platform, _, err := b.localNativeImageGraph(ctx, image.Target)
	if err != nil {
		return imagejob.LocalTarget{}, "", localImageContentError(err)
	}
	return localImageTarget(image.Target), platform, nil
}

func localImageTarget(d ocispec.Descriptor) imagejob.LocalTarget {
	return imagejob.LocalTarget{Digest: d.Digest.String(), Size: d.Size, MediaType: d.MediaType}
}

// Keep source NotFound separate from a present image's incomplete graph.
// Cancellation and service failures preserve their original typed error.
func localImageContentError(err error) error {
	if errdefs.IsNotFound(err) {
		return fmt.Errorf("existing local image content is unavailable: %v", err)
	}
	return err
}

// localRetain is entered only by an original permitted ImageJobs Build call.
// A returned partial pin identifies only this job's intended private resources;
// an error never acknowledges a lost lease/alias reply or permits consumption.
func (b *NativeImageBackend) localRetain(ctx context.Context, r ImageRecipe, p ImagePhase) (PinnedImage, error) {
	if err := r.ValidateLocalRetain(); err != nil {
		return PinnedImage{}, err
	}
	if b == nil || b.Containerd == nil {
		return PinnedImage{}, errors.New("local image retention requires containerd")
	}
	native := platforms.Format(platforms.Normalize(platforms.DefaultSpec()))
	if r.Platform != native {
		return PinnedImage{}, errors.New("local image retention requires the native platform")
	}
	private, err := canonicalImage(p.PreparedRef)
	id, owned := ownedAttemptName(private)
	if err != nil || !owned || id != p.JobID || p.PrivateVersion != 1 || p.PreparedRef != "yoloctl/prepared/"+p.JobID || p.SolveRef != p.JobID {
		return PinnedImage{}, errors.New("local image retention requires its original private job identity")
	}
	ctx, cancel := context.WithTimeout(imageContext(ctx), localImageTimeout)
	defer cancel()
	readSource := func() (images.Image, error) {
		if err := ctx.Err(); err != nil {
			return images.Image{}, err
		}
		image, err := b.Containerd.ImageService().Get(ctx, r.BaseReference)
		if err != nil {
			return images.Image{}, err
		}
		if image.Name != r.BaseReference || localImageTarget(image.Target) != *r.LocalTarget {
			return images.Image{}, errors.New("local image source target changed before retention")
		}
		return image, nil
	}
	source, err := readSource()
	if err != nil {
		return PinnedImage{}, err
	}
	platform, graph, err := b.localNativeImageGraph(ctx, source.Target)
	if err != nil {
		return PinnedImage{}, localImageContentError(err)
	}
	if platform != r.Platform {
		return PinnedImage{}, errors.New("local image config platform differs from the frozen recipe")
	}
	// Repeat the exact target observation after graph validation, immediately
	// before the first effect. The earlier read supplied no lease or authority.
	if _, err := readSource(); err != nil {
		return PinnedImage{}, err
	}
	result := PinnedImage{JobID: p.JobID, Reference: p.PreparedRef, Digest: r.BaseDigest, Lease: "yoloctl-image-" + p.JobID}
	lease, err := b.Containerd.LeasesService().Create(ctx, leases.WithID(result.Lease))
	if err != nil {
		return result, err
	}
	if lease.ID != result.Lease {
		return result, errors.New("local image lease acknowledgement differs from its job")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := b.walkPin(ctx, lease, source.Target, true); err != nil {
		return result, err
	}
	if _, err := readSource(); err != nil {
		return result, err
	}
	// Copy only the target. Public labels/names are never copied or updated.
	created, err := b.Containerd.ImageService().Create(ctx, images.Image{Name: private, Target: source.Target})
	if err != nil {
		return result, err
	}
	if created.Name != private || localImageTarget(created.Target) != *r.LocalTarget {
		return result, errors.New("local image private alias acknowledgement differs from its target")
	}
	if err := b.Validate(ctx, result); err != nil {
		return result, err
	}
	retained, err := b.Containerd.ImageService().Get(ctx, private)
	if err != nil {
		return result, err
	}
	if retained.Name != private || localImageTarget(retained.Target) != *r.LocalTarget {
		return result, errors.New("local image private target changed during validation")
	}
	resources, err := b.Containerd.LeasesService().ListResources(ctx, lease)
	if err != nil {
		return result, err
	}
	for _, resource := range resources {
		if resource.Type == "content" {
			delete(graph, digest.Digest(resource.ID))
		}
	}
	if len(graph) != 0 {
		return result, errors.New("local image lease lacks required graph content")
	}
	if _, err := readSource(); err != nil {
		return result, err
	}
	return result, nil
}

// localMetadataProvider bounds metadata reads performed by containerd's native
// image parsers. Layer contents are checked by descriptor/Info and never read.
type localMetadataProvider struct{ store content.Store }

func (p localMetadataProvider) ReaderAt(ctx context.Context, d ocispec.Descriptor) (content.ReaderAt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.Digest.Validate() != nil || d.Size <= 0 || d.Size > localImageMetadataLimit {
		return nil, errors.New("local image metadata descriptor is invalid or exceeds its bound")
	}
	info, err := p.store.Info(ctx, d.Digest)
	if err != nil {
		return nil, err
	}
	if info.Digest != d.Digest || info.Size != d.Size {
		return nil, errors.New("local image metadata size mismatch")
	}
	reader, err := p.store.ReaderAt(ctx, d)
	if err != nil {
		return nil, err
	}
	if reader.Size() != d.Size {
		_ = reader.Close()
		return nil, errors.New("local image metadata reader size mismatch")
	}
	return reader, nil
}

func (b *NativeImageBackend) localNativeImageGraph(ctx context.Context, target ocispec.Descriptor) (string, map[digest.Digest]imagejob.LocalTarget, error) {
	return localImageGraphForPlatform(ctx, b.Containerd.ContentStore(), target, platforms.DefaultSpec(), platforms.Default())
}

// selectedImageChildren follows containerd's reader ordering at each index and
// requires only that selected branch. Platform filtering never applies to a
// manifest's required config/layers, including unexpected Platform fields there.
func selectedImageChildren(ctx context.Context, provider content.Provider, d ocispec.Descriptor, platform platforms.MatchComparer) ([]ocispec.Descriptor, error) {
	children := images.ChildrenHandler(provider)
	if images.IsIndexType(d.MediaType) {
		if platform == nil {
			return nil, errors.New("image index selection requires its platform matcher")
		}
		children = images.LimitManifests(images.FilterPlatforms(children, platform), platform, 1)
	}
	return children(ctx, d)
}

func (b *NativeImageBackend) walkPinForPlatform(ctx context.Context, l leases.Lease, d ocispec.Descriptor, add bool, platform platforms.MatchComparer) error {
	return images.Walk(ctx, images.HandlerFunc(func(ctx context.Context, d ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		info, err := b.Containerd.ContentStore().Info(ctx, d.Digest)
		if err != nil {
			return nil, err
		}
		if info.Size != d.Size {
			return nil, errors.New("retained content size mismatch")
		}
		if add {
			if err := b.Containerd.LeasesService().AddResource(ctx, l, leases.Resource{Type: "content", ID: d.Digest.String()}); err != nil {
				return nil, err
			}
		}
		return selectedImageChildren(ctx, b.Containerd.ContentStore(), d, platform)
	}), d)
}

func localImageGraphForPlatform(ctx context.Context, store content.Store, target ocispec.Descriptor, native ocispec.Platform, platform platforms.MatchComparer) (string, map[digest.Digest]imagejob.LocalTarget, error) {
	if !images.IsManifestType(target.MediaType) && !images.IsIndexType(target.MediaType) || target.Size <= 0 {
		return "", nil, errors.New("local image target must be a positive manifest or index descriptor")
	}
	provider := localMetadataProvider{store: store}
	graph := map[digest.Digest]imagejob.LocalTarget{}
	stack := []ocispec.Descriptor{target}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if d.Digest.Validate() != nil || d.Size < 0 {
			return "", nil, errors.New("local image graph has an invalid descriptor")
		}
		if previous, found := graph[d.Digest]; found {
			if previous != localImageTarget(d) {
				return "", nil, errors.New("local image graph has inconsistent descriptors")
			}
			continue
		}
		if len(graph) >= localImageGraphLimit {
			return "", nil, errors.New("local image content graph exceeds its bound")
		}
		info, err := provider.store.Info(ctx, d.Digest)
		if err != nil {
			return "", nil, err
		}
		if info.Digest != d.Digest || info.Size != d.Size {
			return "", nil, errors.New("local image graph content size mismatch")
		}
		if !images.IsManifestType(d.MediaType) && !images.IsIndexType(d.MediaType) && !images.IsConfigType(d.MediaType) && !images.IsLayerType(d.MediaType) {
			return "", nil, errors.New("local image graph has an unsupported descriptor type")
		}
		if !images.IsLayerType(d.MediaType) && (d.Size <= 0 || d.Size > localImageMetadataLimit) {
			return "", nil, errors.New("local image graph metadata exceeds its descriptor bound")
		}
		graph[d.Digest] = localImageTarget(d)
		children, err := selectedImageChildren(ctx, provider, d, platform)
		if err != nil {
			return "", nil, err
		}
		stack = append(stack, children...)
	}
	config, err := images.Config(ctx, provider, target, platform)
	if err != nil {
		return "", nil, err
	}
	if !images.IsConfigType(config.MediaType) {
		return "", nil, errors.New("local image manifest lacks an image config")
	}
	actual, err := images.ConfigPlatform(ctx, provider, config)
	if err != nil {
		return "", nil, err
	}
	if actual.OS == "" || actual.Architecture == "" || !platforms.OnlyStrict(native).Match(actual) {
		return "", nil, errors.New("local image config does not match the native platform")
	}
	return platforms.Format(platforms.Normalize(actual)), graph, nil
}
