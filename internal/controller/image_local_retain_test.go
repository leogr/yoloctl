package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	cd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/moby/buildkit/util/contentutil"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const localRetainSource = "docker.io/library/cached:latest"

// These finite services use containerd's supported in-process client adapter,
// with real digest-checked OCI content in BuildKit's memory content store.
// The image/lease services reject unexpected namespaces and unbounded calls.
type localRetainImages struct {
	images.Store
	values      map[string]images.Image
	gets        int
	sourceGets  int
	creates     int
	updates     int
	getError    error
	createError error
	beforeGet   func(string, int)
}

func localRetainCall(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ns, err := namespaces.NamespaceRequired(ctx)
	if err != nil || ns != "default" {
		return errors.New("local retain adapter left the system containerd namespace")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return errors.New("local retain adapter made an unbounded service call")
	}
	return nil
}

func (s *localRetainImages) Get(ctx context.Context, name string) (images.Image, error) {
	s.gets++
	if err := localRetainCall(ctx); err != nil {
		return images.Image{}, err
	}
	if name == localRetainSource {
		s.sourceGets++
	}
	if s.beforeGet != nil {
		s.beforeGet(name, s.sourceGets)
	}
	if s.getError != nil {
		return images.Image{}, s.getError
	}
	i, exists := s.values[name]
	if !exists {
		return images.Image{}, errdefs.ErrNotFound
	}
	return i, nil
}
func (s *localRetainImages) Create(ctx context.Context, image images.Image) (images.Image, error) {
	s.creates++
	if err := localRetainCall(ctx); err != nil {
		return images.Image{}, err
	}
	if _, owned := ownedAttemptName(image.Name); !owned || len(image.Labels) != 0 {
		return images.Image{}, errors.New("local retain attempted public publication or copied labels")
	}
	if _, exists := s.values[image.Name]; exists {
		return images.Image{}, errdefs.ErrAlreadyExists
	}
	s.values[image.Name] = image
	return image, s.createError
}
func (s *localRetainImages) Update(context.Context, images.Image, ...string) (images.Image, error) {
	s.updates++
	return images.Image{}, errors.New("local retain must not update a public source or adopt an alias")
}
func (s *localRetainImages) List(ctx context.Context, _ ...string) ([]images.Image, error) {
	if err := localRetainCall(ctx); err != nil {
		return nil, err
	}
	var all []images.Image
	for _, image := range s.values {
		all = append(all, image)
	}
	return all, nil
}
func (s *localRetainImages) Delete(ctx context.Context, name string, _ ...images.DeleteOpt) error {
	if err := localRetainCall(ctx); err != nil {
		return err
	}
	if _, owned := ownedAttemptName(name); !owned {
		return errors.New("local retain cleanup attempted to delete a public source")
	}
	delete(s.values, name)
	return nil
}

type localRetainLeases struct {
	leases.Manager
	values      map[string]map[leases.Resource]bool
	creates     int
	adds        int
	reads       int
	createError error
	addError    error
	onCreate    func()
	omitContent digest.Digest
}

func (s *localRetainLeases) Create(ctx context.Context, options ...leases.Opt) (leases.Lease, error) {
	s.creates++
	if err := localRetainCall(ctx); err != nil {
		return leases.Lease{}, err
	}
	var lease leases.Lease
	for _, option := range options {
		if err := option(&lease); err != nil {
			return leases.Lease{}, err
		}
	}
	if _, exists := s.values[lease.ID]; exists {
		return leases.Lease{}, errdefs.ErrAlreadyExists
	}
	s.values[lease.ID] = map[leases.Resource]bool{}
	if s.onCreate != nil {
		s.onCreate()
	}
	return lease, s.createError
}
func (s *localRetainLeases) AddResource(ctx context.Context, lease leases.Lease, resource leases.Resource) error {
	s.adds++
	if err := localRetainCall(ctx); err != nil {
		return err
	}
	resources, exists := s.values[lease.ID]
	if !exists {
		return errdefs.ErrNotFound
	}
	if resource.Type != "content" || resource.ID != s.omitContent.String() {
		resources[resource] = true
	}
	return s.addError
}
func (s *localRetainLeases) ListResources(ctx context.Context, lease leases.Lease) ([]leases.Resource, error) {
	s.reads++
	if err := localRetainCall(ctx); err != nil {
		return nil, err
	}
	resources, exists := s.values[lease.ID]
	if !exists {
		return nil, errdefs.ErrNotFound
	}
	var all []leases.Resource
	for resource := range resources {
		all = append(all, resource)
	}
	return all, nil
}
func (s *localRetainLeases) Delete(ctx context.Context, lease leases.Lease, _ ...leases.DeleteOpt) error {
	if err := localRetainCall(ctx); err != nil {
		return err
	}
	delete(s.values, lease.ID)
	return nil
}

type localRetainContent struct {
	content.Store
	missing, wrongSize digest.Digest
}

func (s *localRetainContent) Info(ctx context.Context, id digest.Digest) (content.Info, error) {
	if id == s.missing {
		return content.Info{}, errdefs.ErrNotFound
	}
	info, err := s.Store.Info(ctx, id)
	if err == nil && id == s.wrongSize {
		info.Size++
	}
	return info, err
}

type localRetainFixture struct {
	backend *NativeImageBackend
	images  *localRetainImages
	leases  *localRetainLeases
	content *localRetainContent
	target  ocispec.Descriptor
	config  ocispec.Descriptor
	layer   ocispec.Descriptor
	phase   ImagePhase
}

func newLocalRetainFixture(t *testing.T, index, wrongPlatform bool) localRetainFixture {
	t.Helper()
	native := platforms.Normalize(platforms.DefaultSpec())
	other := native
	other.Architecture = "arm64"
	if native.Architecture == other.Architecture {
		other.Architecture = "amd64"
	}
	return newLocalRetainPlatformFixture(t, index, wrongPlatform, native, other)
}

func newLocalRetainPlatformFixture(t *testing.T, index, wrongPlatform bool, native, other ocispec.Platform) localRetainFixture {
	t.Helper()
	store := contentutil.NewBuffer()
	blob := func(media string, raw []byte) ocispec.Descriptor {
		d := ocispec.Descriptor{MediaType: media, Digest: digest.FromBytes(raw), Size: int64(len(raw))}
		if err := content.WriteBlob(t.Context(), store, d.Digest.String(), bytes.NewReader(raw), d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	jsonBlob := func(media string, value any) ocispec.Descriptor {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return blob(media, raw)
	}
	configPlatform := native
	if wrongPlatform {
		configPlatform = other
	}
	layer := blob(ocispec.MediaTypeImageLayer, []byte("finite local image layer"))
	config := jsonBlob(ocispec.MediaTypeImageConfig, ocispec.Image{Platform: configPlatform, RootFS: ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{layer.Digest}}})
	target := jsonBlob(ocispec.MediaTypeImageManifest, ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest, Config: config, Layers: []ocispec.Descriptor{layer}})
	if index {
		selected := target
		selected.Platform = &native
		foreign := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digest.FromString("absent foreign content"), Size: 999, Platform: &other}
		target = jsonBlob(ocispec.MediaTypeImageIndex, ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex, Manifests: []ocispec.Descriptor{foreign, selected}})
	}
	i := &localRetainImages{values: map[string]images.Image{localRetainSource: {Name: localRetainSource, Target: target, Labels: map[string]string{"preserve": "public"}}}}
	l := &localRetainLeases{values: map[string]map[leases.Resource]bool{}}
	c := &localRetainContent{Store: store}
	client, err := cd.New("", cd.WithServices(cd.WithImageStore(i), cd.WithContentStore(c), cd.WithLeasesService(l)))
	if err != nil {
		t.Fatal(err)
	}
	b := &NativeImageBackend{Containerd: client, EnsureBuilder: func(context.Context) error {
		t.Error("local retain reached EnsureBuilder")
		return errors.New("builder forbidden in local retain fixture")
	}}
	t.Cleanup(b.Close)
	id := strings.Repeat("a", 64)
	return localRetainFixture{backend: b, images: i, leases: l, content: c, target: target, config: config, layer: layer,
		phase: ImagePhase{PrivateVersion: 1, JobID: id, PreparedRef: "yoloctl/prepared/" + id, SolveRef: id}}
}

func (f localRetainFixture) recipe(t *testing.T) ImageRecipe {
	t.Helper()
	r, err := imagejob.NewLocalRetain(localRetainSource, platforms.Format(platforms.DefaultSpec()), localImageTarget(f.target))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLocalRetainBackendResolvesTargetPinsNativeGraphAndReleasesOnlyPrivateResources(t *testing.T) {
	for _, index := range []bool{false, true} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			f := newLocalRetainFixture(t, index, false)
			target, platform, err := f.backend.ResolveLocal(t.Context(), "cached")
			if err != nil || target != localImageTarget(f.target) || target.Digest == f.config.Digest.String() || platform != platforms.Format(platforms.Normalize(platforms.DefaultSpec())) || f.leases.creates != 0 || f.images.creates != 0 {
				t.Fatal("local read returned config ID, wrong platform or effects", target, platform, err)
			}
			pin, err := f.backend.Build(t.Context(), f.recipe(t), f.phase)
			if err != nil || pin.Digest != target.Digest || pin.Lease != "yoloctl-image-"+f.phase.JobID || f.leases.creates != 1 || f.images.creates != 1 || f.images.updates != 0 {
				t.Fatal("local retained output differs or used publication", pin, err)
			}
			wantResources := 3
			if index {
				wantResources++
			}
			if len(f.leases.values[pin.Lease]) != wantResources || len(f.images.values) != 2 || f.images.values[localRetainSource].Labels["preserve"] != "public" {
				t.Fatal("native graph is unleased or public source changed", f.leases.values, f.images.values)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := f.backend.Validate(ctx, pin); err != nil {
				t.Fatal("retained pin was unusable", err)
			}
			if err := f.backend.Release(ctx, pin); err != nil || len(f.leases.values) != 0 || len(f.images.values) != 1 || f.images.values[localRetainSource].Target.Digest != f.target.Digest {
				t.Fatal("owned release changed the public source or retained private state", err)
			}
		})
	}
}

func TestSelectedNativeGraphDoesNotRequireAbsentCompatibleAlternatives(t *testing.T) {
	for _, family := range []struct {
		native, compatible ocispec.Platform
	}{
		{ocispec.Platform{OS: "linux", Architecture: "amd64"}, ocispec.Platform{OS: "linux", Architecture: "386"}},
		{ocispec.Platform{OS: "linux", Architecture: "arm64"}, ocispec.Platform{OS: "linux", Architecture: "arm", Variant: "v7"}},
	} {
		t.Run(platforms.Format(family.native), func(t *testing.T) {
			matcher := platforms.Only(family.native)
			if !matcher.Match(family.compatible) {
				t.Fatal("fixture does not exercise a matching compatibility alternative")
			}
			for _, missing := range []string{"none", "selected-config", "selected-layer"} {
				t.Run(missing, func(t *testing.T) {
					// The absent compatibility alternative appears before the native
					// branch, so selecting the first listed entry is also incorrect.
					f := newLocalRetainPlatformFixture(t, true, false, family.native, family.compatible)
					switch missing {
					case "selected-config":
						f.content.missing = f.config.Digest
					case "selected-layer":
						f.content.missing = f.layer.Digest
					}
					ctx, cancel := context.WithTimeout(imageContext(t.Context()), time.Second)
					defer cancel()
					platform, graph, err := localImageGraphForPlatform(ctx, f.content, f.target, family.native, matcher)
					if missing != "none" {
						if err == nil {
							t.Fatal("selected native content was optional", platform, graph)
						}
						if err := f.backend.walkPinForPlatform(ctx, leases.Lease{}, f.target, false, matcher); err == nil {
							t.Fatal("validation accepted missing selected config/layers")
						}
						if f.leases.creates != 0 || f.leases.adds != 0 || f.images.creates != 0 {
							t.Fatal("read-only missing-content control acquired authority")
						}
						return
					}
					if err != nil || platform != platforms.Format(platforms.Normalize(family.native)) || len(graph) != 4 {
						t.Fatal("resolution required an unselected compatibility branch", platform, graph, err)
					}
					lease, err := f.backend.Containerd.LeasesService().Create(ctx, leases.WithID("yoloctl-image-"+f.phase.JobID))
					if err != nil {
						t.Fatal(err)
					}
					if err := f.backend.walkPinForPlatform(ctx, lease, f.target, true, matcher); err != nil {
						t.Fatal("pinning required an unselected compatibility branch", err)
					}
					if err := f.backend.walkPinForPlatform(ctx, lease, f.target, false, matcher); err != nil {
						t.Fatal("validation required an unselected compatibility branch", err)
					}
					if len(f.leases.values[lease.ID]) != len(graph) {
						t.Fatal("selection differed between resolution and retained resources", graph, f.leases.values[lease.ID])
					}
					for id := range graph {
						if !f.leases.values[lease.ID][leases.Resource{Type: "content", ID: id.String()}] {
							t.Fatal("selected native graph content was not retained", id)
						}
					}
				})
			}
		})
	}
}

func TestLocalRetainActualBackendKeepsOnlySelectedNativeCompatibilityBranch(t *testing.T) {
	native := platforms.Normalize(platforms.DefaultSpec())
	compatible := native
	switch native.Architecture {
	case "amd64":
		compatible.Architecture = "386"
	case "arm64":
		compatible.Architecture, compatible.Variant = "arm", "v7"
	default:
		t.Skip("compatibility alternatives are qualified here for amd64 and arm64")
	}
	if !platforms.Default().Match(compatible) {
		t.Skip("this host's default matcher does not include the compatibility alternative")
	}
	f := newLocalRetainPlatformFixture(t, true, false, native, compatible)
	target, platform, err := f.backend.ResolveLocal(t.Context(), "cached")
	if err != nil || target != localImageTarget(f.target) || platform != platforms.Format(native) {
		t.Fatal("actual local resolution required absent compatibility content", target, platform, err)
	}
	pin, err := f.backend.Build(t.Context(), f.recipe(t), f.phase)
	if err != nil || len(f.leases.values[pin.Lease]) != 4 {
		t.Fatal("actual local retention required absent compatibility content", pin, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := f.backend.Validate(ctx, pin); err != nil {
		t.Fatal("actual private validation selected a different graph", err)
	}
	if f.images.creates != 1 || f.images.updates != 0 {
		t.Fatal("compatibility selection triggered repair or public publication")
	}
}

func TestResolveLocalDistinguishesAbsentSourceFromIncompleteOrWrongPlatformImage(t *testing.T) {
	for _, mode := range []string{"absent-source", "missing-layer", "foreign-platform-layer", "wrong-size", "config-id", "wrong-platform", "lying-index-platform", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalRetainFixture(t, mode == "lying-index-platform", mode == "wrong-platform" || mode == "lying-index-platform")
			failure := errors.New("local image service reply lost")
			switch mode {
			case "absent-source":
				delete(f.images.values, localRetainSource)
			case "missing-layer":
				f.content.missing = f.layer.Digest
			case "foreign-platform-layer":
				layer := f.layer
				layer.Digest = digest.FromString("missing layer with a misleading platform")
				layer.Platform = &ocispec.Platform{OS: "unknown", Architecture: "unknown"}
				raw, marshalErr := json.Marshal(ocispec.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest, Config: f.config, Layers: []ocispec.Descriptor{layer}})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				d := ocispec.Descriptor{Digest: digest.FromBytes(raw), Size: int64(len(raw)), MediaType: ocispec.MediaTypeImageManifest}
				if writeErr := content.WriteBlob(t.Context(), f.content, d.Digest.String(), bytes.NewReader(raw), d); writeErr != nil {
					t.Fatal(writeErr)
				}
				i := f.images.values[localRetainSource]
				i.Target = d
				f.images.values[localRetainSource] = i
			case "wrong-size":
				f.content.wrongSize = f.layer.Digest
			case "config-id":
				i := f.images.values[localRetainSource]
				i.Target = f.config
				f.images.values[localRetainSource] = i
			case "unavailable":
				f.images.getError = failure
			}
			_, _, err := f.backend.ResolveLocal(t.Context(), "cached")
			if err == nil || errdefs.IsNotFound(err) != (mode == "absent-source") || mode == "unavailable" && !errors.Is(err, failure) || f.leases.creates != 0 || f.images.creates != 0 {
				t.Fatal("local read conflated absence, graph/platform failure or an effect", err)
			}
		})
	}
}

func TestLocalRetainBackendRevalidatesSourceAndGraphBeforeEffects(t *testing.T) {
	for _, mode := range []string{"digest", "size", "media", "moved-after-graph", "disappeared-after-graph", "missing-layer", "wrong-platform", "mixed-recipe", "private-identity"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalRetainFixture(t, false, mode == "wrong-platform")
			r := f.recipe(t)
			p := f.phase
			switch mode {
			case "digest":
				r.BaseDigest = digest.FromString("changed target").String()
				r.LocalTarget.Digest = r.BaseDigest
			case "size":
				r.LocalTarget.Size++
			case "media":
				r.LocalTarget.MediaType = ocispec.MediaTypeImageIndex
			case "moved-after-graph", "disappeared-after-graph":
				f.images.beforeGet = func(name string, count int) {
					if name == localRetainSource && count == 2 {
						i := f.images.values[name]
						i.Target.Digest = digest.FromString("moved source")
						f.images.values[name] = i
						if mode == "disappeared-after-graph" {
							delete(f.images.values, name)
						}
					}
				}
			case "missing-layer":
				f.content.missing = f.layer.Digest
			case "mixed-recipe":
				r.Force = true
			case "private-identity":
				p.PreparedRef = localRetainSource
			}
			pin, err := f.backend.Build(t.Context(), r, p)
			if err == nil || pin != (PinnedImage{}) || f.leases.creates != 0 || f.images.creates != 0 || f.images.updates != 0 {
				t.Fatal("invalid/drifted local input reached a lease or private alias", pin, err)
			}
		})
	}
}

func TestLocalRetainBackendPreservesLostRepliesPartialOwnershipAndCancellation(t *testing.T) {
	for _, mode := range []string{"lease-reply", "resource-reply", "alias-reply", "cancel-after-lease", "source-moved-after-lease", "existing-alias", "unleased-child"} {
		t.Run(mode, func(t *testing.T) {
			f := newLocalRetainFixture(t, false, false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("original local retain reply lost")
			switch mode {
			case "lease-reply":
				f.leases.createError = failure
			case "resource-reply":
				f.leases.addError = failure
			case "alias-reply":
				f.images.createError = failure
			case "cancel-after-lease":
				f.leases.onCreate = cancel
			case "source-moved-after-lease":
				f.images.beforeGet = func(name string, count int) {
					if name == localRetainSource && count == 3 {
						i := f.images.values[name]
						i.Target.Digest = digest.FromString("moved after lease")
						f.images.values[name] = i
					}
				}
			case "existing-alias":
				private, _ := canonicalImage(f.phase.PreparedRef)
				f.images.values[private] = images.Image{Name: private, Target: f.target}
			case "unleased-child":
				f.leases.omitContent = f.layer.Digest
			}
			pin, err := f.backend.Build(ctx, f.recipe(t), f.phase)
			if err == nil || pin.JobID != f.phase.JobID || pin.Reference != f.phase.PreparedRef || pin.Lease != "yoloctl-image-"+f.phase.JobID || f.leases.creates != 1 || f.images.updates != 0 {
				t.Fatal("unknown local result lost its partial ownership or was adopted/retried", pin, err)
			}
			if strings.HasSuffix(mode, "reply") && !errors.Is(err, failure) || mode == "cancel-after-lease" && !errors.Is(err, context.Canceled) {
				t.Fatal("local retain replaced the original error", err)
			}
			wantCreates := 0
			if mode == "alias-reply" || mode == "existing-alias" || mode == "unleased-child" {
				wantCreates = 1
			}
			wantReads := 0
			if mode == "unleased-child" {
				wantReads = 2
			}
			if f.images.creates != wantCreates || f.leases.reads != wantReads || (mode == "lease-reply" || mode == "cancel-after-lease") && f.leases.adds != 0 || mode == "resource-reply" && f.leases.adds != 1 {
				t.Fatal("failed acknowledgement reached a later effect/validation or replay", f.images.creates, f.leases.adds, f.leases.reads)
			}
		})
	}
}

func TestLocalRetainBackendCancellationBeforeEffects(t *testing.T) {
	f := newLocalRetainFixture(t, false, false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := f.backend.ResolveLocal(ctx, "cached"); !errors.Is(err, context.Canceled) {
		t.Fatal("local resolution ignored cancellation", err)
	}
	if pin, err := f.backend.Build(ctx, f.recipe(t), f.phase); !errors.Is(err, context.Canceled) || pin != (PinnedImage{}) {
		t.Fatal("cancelled local retention returned a pin", pin, err)
	}
	if f.leases.creates != 0 || f.images.creates != 0 {
		t.Fatal("cancelled local retention reached a private effect")
	}
}
