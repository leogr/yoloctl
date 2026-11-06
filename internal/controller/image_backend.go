package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	cd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	"github.com/leogr/yoloctl/internal/imagejob"
	bk "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	"github.com/moby/buildkit/util/grpcerrors"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/tonistiigi/fsutil"
	"google.golang.org/grpc/codes"
)

// NativeImageBackend is used only by the root VM controller. Worker requests
// can describe copied inputs, but cannot supply exporter options or sockets.
type NativeImageBackend struct {
	PreflightBuild func(context.Context, ImageRecipe) error
	EnsureBuilder  func(context.Context) error
	Containerd     *cd.Client
	Builder        *bk.Client
	builderMu      sync.Mutex
	closed         bool
	newBuilder     func(context.Context) (*bk.Client, error)
}

func (b *NativeImageBackend) Close() {
	b.builderMu.Lock()
	b.closed = true
	builder := b.Builder
	b.builderMu.Unlock()
	if builder != nil {
		_ = builder.Close()
	}
	if b.Containerd != nil {
		_ = b.Containerd.Close()
	}
}
func imageContext(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, "default")
}
func canonicalImage(ref string) (string, error) {
	n, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", err
	}
	return reference.TagNameOnly(n).String(), nil
}

// Resolve is read-only. A missing external base is resolved at its registry;
// BuildKit later pulls the exact digest, never the mutable name again.
func (b *NativeImageBackend) Resolve(ctx context.Context, ref string) (string, string, error) {
	if ref == "scratch" {
		return digest.FromString("scratch").String(), platforms.Format(platforms.DefaultSpec()), nil
	}
	name, err := canonicalImage(ref)
	if err != nil {
		return "", "", err
	}
	i, err := b.Containerd.ImageService().Get(imageContext(ctx), name)
	if err == nil {
		return i.Target.Digest.String(), platforms.Format(platforms.DefaultSpec()), nil
	}
	if !errdefs.IsNotFound(err) {
		return "", "", err
	}
	_, d, err := docker.NewResolver(docker.ResolverOptions{}).Resolve(ctx, name)
	if err != nil {
		return "", "", err
	}
	return d.Digest.String(), platforms.Format(platforms.DefaultSpec()), nil
}
func (b *NativeImageBackend) Preflight(ctx context.Context, r ImageRecipe) error {
	if b.PreflightBuild == nil {
		return errors.New("image preflight unavailable")
	}
	return b.PreflightBuild(ctx, r)
}

// privateSolveOptions is a closed exporter policy, checked before submission.
// No client-provided exporter/cache/alias option reaches BuildKit.
func privateSolveOptions(p ImagePhase, dir string, r ImageRecipe) (bk.SolveOpt, error) {
	if p.PrivateVersion != 1 || p.PreparedRef != "yoloctl/prepared/"+p.JobID || p.JobID == "" {
		return bk.SolveOpt{}, errors.New("invalid private attempt")
	}
	outputName, err := canonicalImage(p.PreparedRef)
	if err != nil {
		return bk.SolveOpt{}, err
	}
	mount, err := fsutil.NewFS(dir)
	if err != nil {
		return bk.SolveOpt{}, err
	}
	attrs := map[string]string{"platform": r.Platform, "image-resolve-mode": "local"}
	if r.NoCache {
		attrs["no-cache"] = ""
	}
	for k, v := range r.Args {
		attrs["build-arg:"+k] = v
	}
	if r.Force {
		attrs["build-arg:YOLOCTL_IMAGE_REFRESH"] = p.Attempt
	}
	opt := bk.SolveOpt{Ref: p.SolveRef, Frontend: "dockerfile.v0", FrontendAttrs: attrs,
		LocalMounts:  map[string]fsutil.FS{"context": mount, "dockerfile": mount},
		Exports:      []bk.ExportEntry{{Type: bk.ExporterImage, Attrs: privateExporterAttrs(outputName)}},
		CacheExports: []bk.CacheOptionsEntry{{Type: "inline"}}}
	policy, err := imageSourcePolicy(r)
	if err != nil {
		return bk.SolveOpt{}, err
	}
	opt.SourcePolicy = policy
	stageContexts, err := dockerfileStageContexts(r.Dockerfile)
	if err != nil {
		return bk.SolveOpt{}, err
	}
	// Hand each converted image to the Dockerfile frontend as a named context
	// too, so it resolves the immutable locator directly. Daemons that deny the
	// mutable name during metadata resolution before applying a CONVERT would
	// otherwise refuse a correctly pinned input.
	for _, rule := range policy.Rules {
		if rule.Action != spb.PolicyAction_CONVERT || rule.Selector == nil || rule.Updates == nil {
			continue
		}
		original, ok := strings.CutPrefix(rule.Selector.Identifier, "docker-image://")
		if !ok {
			continue
		}
		named, err := reference.ParseNormalizedNamed(original)
		if err != nil {
			return bk.SolveOpt{}, err
		}
		name := strings.TrimSuffix(reference.FamiliarString(named), ":latest")
		// BuildKit checks a stage alias before its base image. A matching
		// context replaces that entire stage and discards its instructions.
		// Colliding images retain the exact source-policy conversion; only
		// unambiguous external names get the additional metadata shortcut.
		if !stageContexts[name] {
			attrs["context:"+name] = rule.Updates.Identifier
		}
	}
	return opt, validatePrivateExport(opt, p)
}

// privateExporterAttrs is the complete, closed exporter option set for a
// private image: stored and unpacked in this VM's containerd, never pushed.
// Its newly built layers are written uncompressed, because they are unpacked
// in the same VM right away and never leave it; gzip only cost CPU on both
// ends. Already cached layers keep their recorded compression.
func privateExporterAttrs(outputName string) map[string]string {
	return map[string]string{"name": outputName, "push": "false", "name-canonical": "false", "store": "true", "unpack": "true", "compression": "uncompressed"}
}

func validatePrivateExport(o bk.SolveOpt, p ImagePhase) error {
	if len(o.Exports) != 1 || o.Exports[0].Type != bk.ExporterImage || o.Exports[0].Output != nil || o.Exports[0].OutputDir != "" || o.Exports[0].OutputStore != nil || len(o.CacheExports) != 1 || o.CacheExports[0].Type != "inline" || len(o.CacheExports[0].Attrs) != 0 || len(o.CacheImports) != 0 {
		return errors.New("unsupported image export")
	}
	outputName, err := canonicalImage(p.PreparedRef)
	if err != nil {
		return err
	}
	expected := privateExporterAttrs(outputName)
	if len(o.Exports[0].Attrs) != len(expected) {
		return errors.New("unsupported private exporter option")
	}
	for k, v := range expected {
		if o.Exports[0].Attrs[k] != v {
			return errors.New("private exporter option mismatch")
		}
	}
	return nil
}

func (b *NativeImageBackend) Build(ctx context.Context, r ImageRecipe, p ImagePhase) (PinnedImage, error) {
	if r.Builder == imagejob.LocalRetainBuilder {
		return b.localRetain(ctx, r, p)
	}
	if r.Builder == "pull/1" {
		return b.pullPrivate(ctx, r, p)
	}
	builder, err := b.readyBuilder(ctx)
	if err != nil {
		return PinnedImage{}, err
	}
	dir, err := os.MkdirTemp("", "yoloctl-image-")
	if err != nil {
		return PinnedImage{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0755); err != nil {
		return PinnedImage{}, err
	}
	ctx = imageContext(ctx)
	lease, err := b.Containerd.LeasesService().Create(ctx, leases.WithID("yoloctl-image-"+p.JobID))
	if err != nil {
		return PinnedImage{}, err
	}
	result := PinnedImage{JobID: p.JobID, Reference: p.PreparedRef, Lease: lease.ID}
	inputs := append([]imagejob.ImageInput(nil), r.Images...)
	if len(inputs) == 0 && r.BaseReference != "" && r.BaseReference != "scratch" {
		inputs = []imagejob.ImageInput{{Reference: r.BaseReference, Source: r.BaseReference, Digest: r.BaseDigest, Platform: r.Platform}}
	}
	uses := map[string]int{}
	for _, input := range inputs {
		uses[input.Reference]++
	}
	for i, dependency := range inputs {
		source := dependency.Source
		if source == "" {
			source = dependency.Reference
		}
		inputName, err := canonicalImage(source)
		if err != nil {
			return result, err
		}
		input, err := b.Containerd.ImageService().Get(ctx, inputName)
		if err == nil {
			if input.Target.Digest.String() != dependency.Digest {
				if _, private := ownedAttemptName(input.Name); private {
					return result, errors.New("private image dependency moved before preparation")
				}
				// The requested registry index may differ from a native-only
				// local tag. The source policy still requires the exact digest.
				continue
			}
			if err = b.walkPin(ctx, lease, input.Target, true); err != nil {
				return result, err
			}
			if uses[dependency.Reference] > 1 {
				continue
			}
			alias := fmt.Sprintf("%s-input-%d", p.PreparedRef, i)
			input.Name, _ = canonicalImage(alias)
			if _, err = b.Containerd.ImageService().Create(ctx, input); err != nil {
				return result, err
			}
			inputs[i].Source = alias
		} else if !errdefs.IsNotFound(err) {
			return result, err
		}
	}
	r.Images = inputs

	df, err := immutableDockerfile(r)
	if err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(dir, "Dockerfile"), df, 0644); err != nil {
		return result, err
	}
	for name, data := range r.Files {
		dest := filepath.Join(dir, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return result, err
		}
		mode := os.FileMode(0755)
		if m, ok := r.FileModes[name]; ok {
			mode = os.FileMode(m)
		}
		if err = os.WriteFile(dest, data, mode); err != nil {
			return result, err
		}
	}
	opt, err := privateSolveOptions(p, dir, r)
	if err != nil {
		return result, &ImageBuildFailure{Cause: err}
	}
	response, err := solveImageWithProgress(ctx, builder, opt)
	if err != nil {
		return result, classifyImageSolveError(ctx, err)
	}
	got, err := canonicalImage(response.ExporterResponse[exptypes.ExporterImageNameKey])
	want, _ := canonicalImage(p.PreparedRef)
	if err != nil || got != want {
		return result, errors.New("unexpected exported image name")
	}
	encoded := response.ExporterResponse[exptypes.ExporterImageDescriptorKey]
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return result, err
	}
	var desc ocispec.Descriptor
	if err = json.Unmarshal(raw, &desc); err != nil {
		return result, err
	}
	if err = desc.Digest.Validate(); err != nil {
		return result, err
	}
	ctx = imageContext(ctx)
	image, err := b.Containerd.ImageService().Get(ctx, want)
	if err != nil {
		return result, err
	}
	if image.Target.Digest != desc.Digest || image.Target.Size != desc.Size || image.Target.MediaType != desc.MediaType {
		return result, errors.New("export target descriptor mismatch")
	}
	result.Digest = desc.Digest.String()
	if err = b.walkPin(ctx, lease, desc, true); err != nil {
		return result, err
	}
	return result, nil
}
func (b *NativeImageBackend) walkPin(ctx context.Context, l leases.Lease, d ocispec.Descriptor, add bool) error {
	return b.walkPinForPlatform(ctx, l, d, add, platforms.Default())
}
func (b *NativeImageBackend) Validate(ctx context.Context, p PinnedImage) (err error) {
	// Validation is read-only and always bounded here, including the job's
	// detached post-build check and consumers without an outer deadline.
	ctx, cancel := context.WithTimeout(imageContext(ctx), localImageTimeout)
	defer func() {
		err = errors.Join(err, ctx.Err())
		cancel()
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	name, err := canonicalImage(p.Reference)
	if err != nil {
		return err
	}
	image, err := b.Containerd.ImageService().Get(ctx, name)
	if err != nil {
		return err
	}
	if image.Target.Digest != digest.Digest(p.Digest) {
		return errors.New("private image pin changed")
	}
	resources, err := b.Containerd.LeasesService().ListResources(ctx, leases.Lease{ID: p.Lease})
	if err != nil {
		return err
	}
	found := false
	for _, r := range resources {
		if r.Type == "content" && r.ID == p.Digest {
			found = true
		}
	}
	if !found {
		return errors.New("image lease lacks target content")
	}
	return b.walkPin(ctx, leases.Lease{ID: p.Lease}, image.Target, false)
}
func (b *NativeImageBackend) Release(ctx context.Context, p PinnedImage) error {
	ctx = imageContext(ctx)
	name, err := canonicalImage(p.Reference)
	if err != nil {
		return err
	}
	var e error
	all, listErr := b.Containerd.ImageService().List(ctx)
	if listErr != nil {
		e = listErr
	} else {
		for _, image := range all {
			if id, owned := ownedAttemptName(image.Name); owned && id == p.JobID {
				if err := b.Containerd.ImageService().Delete(ctx, image.Name); err != nil && !errdefs.IsNotFound(err) {
					e = errors.Join(e, err)
				}
			}
		}
	}
	_ = name
	err = b.Containerd.LeasesService().Delete(ctx, leases.Lease{ID: p.Lease})
	if errdefs.IsNotFound(err) {
		err = nil
	}
	return errors.Join(e, err)
}

// Publish is invoked only after the requesting execution's own apply gate.
func (b *NativeImageBackend) Publish(ctx context.Context, p PinnedImage, target string) error {
	if err := b.Validate(ctx, p); err != nil {
		return err
	}
	name, err := canonicalImage(target)
	if err != nil {
		return err
	}
	if err := ValidatePublicImageReference(target); err != nil {
		return err
	}
	ctx = imageContext(ctx)
	source, _ := canonicalImage(p.Reference)
	i, err := b.Containerd.ImageService().Get(ctx, source)
	if err != nil {
		return err
	}
	i.Name = name
	if _, err = b.Containerd.ImageService().Update(ctx, i, "target", "labels"); errdefs.IsNotFound(err) {
		_, err = b.Containerd.ImageService().Create(ctx, i)
	}
	if err != nil {
		return fmt.Errorf("publish image: %w", err)
	}
	return nil
}

// The pull resolver changes only the destination name. Fetching remains bound
// to the exact immutable source selected at prepare; no public alias is made.
type privatePullResolver struct {
	remotes.Resolver
	source, name string
	target       digest.Digest
}

func (r privatePullResolver) Resolve(ctx context.Context, _ string) (string, ocispec.Descriptor, error) {
	_, d, err := r.Resolver.Resolve(ctx, r.source)
	if err == nil && d.Digest != r.target {
		err = errors.New("pull target digest changed")
	}
	return r.name, d, err
}
func (r privatePullResolver) Fetcher(ctx context.Context, _ string) (remotes.Fetcher, error) {
	return r.Resolver.Fetcher(ctx, r.source)
}
func (b *NativeImageBackend) pullPrivate(ctx context.Context, r ImageRecipe, p ImagePhase) (PinnedImage, error) {
	ctx = imageContext(ctx)
	name, err := canonicalImage(p.PreparedRef)
	if err != nil {
		return PinnedImage{}, err
	}
	n, err := reference.ParseNormalizedNamed(r.BaseReference)
	if err != nil {
		return PinnedImage{}, err
	}
	source := reference.TrimNamed(n).String() + "@" + r.BaseDigest
	lease, err := b.Containerd.LeasesService().Create(ctx, leases.WithID("yoloctl-image-"+p.JobID))
	if err != nil {
		return PinnedImage{}, err
	}
	result := PinnedImage{JobID: p.JobID, Reference: p.PreparedRef, Digest: r.BaseDigest, Lease: lease.ID}
	resolver := privatePullResolver{Resolver: docker.NewResolver(docker.ResolverOptions{}), source: source, name: name, target: digest.Digest(r.BaseDigest)}
	image, err := b.Containerd.Pull(leases.WithLease(ctx, lease.ID), source, cd.WithResolver(resolver), cd.WithPullUnpack)
	if err != nil {
		return result, err
	}
	if image.Target().Digest != digest.Digest(r.BaseDigest) {
		return result, errors.New("private pull digest mismatch")
	}
	return result, nil
}

func ValidatePublicImageReference(ref string) error {
	name, err := canonicalImage(ref)
	if err != nil {
		return err
	}
	if strings.HasPrefix(name, "docker.io/yoloctl/prepared/") {
		return errors.New("yoloctl/prepared is reserved for private image attempts")
	}
	return nil
}
func ownedAttemptName(name string) (string, bool) {
	tail, ok := strings.CutPrefix(name, "docker.io/yoloctl/prepared/")
	if !ok {
		return "", false
	}
	tail = strings.TrimSuffix(tail, ":latest")
	if before, after, ok := strings.Cut(tail, "-input-"); ok {
		if _, err := strconv.ParseUint(after, 10, 32); err != nil {
			return "", false
		}
		tail = before
	} else {
		tail = strings.TrimSuffix(tail, "-input")
	}
	if len(tail) != 64 {
		return "", false
	}
	for _, c := range tail {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return tail, true
}

// Sweep runs only after durable recovery, before new consumers are admitted.
// It removes this controller's disposable names/leases, preserving legacy
// effects whose private isolation was never established. Orphan warnings and
// execution receipts remain; deleting a name proves nothing about a survivor.
func (b *NativeImageBackend) Sweep(ctx context.Context, records []Receipt) error {
	keep := map[string]bool{}
	for _, r := range records {
		if r.Image != nil && r.Image.PrivateVersion != 1 && (r.unresolved() || r.reserving()) {
			keep[r.ID] = true
		}
	}
	ctx = imageContext(ctx)
	all, err := b.Containerd.ImageService().List(ctx)
	if err != nil {
		return err
	}
	for _, i := range all {
		if id, owned := ownedAttemptName(i.Name); owned && !keep[id] {
			if err := b.Containerd.ImageService().Delete(ctx, i.Name); err != nil && !errdefs.IsNotFound(err) {
				return err
			}
		}
	}
	list, err := b.Containerd.LeasesService().List(ctx)
	if err != nil {
		return err
	}
	for _, l := range list {
		id, ok := strings.CutPrefix(l.ID, "yoloctl-image-")
		if !ok {
			continue
		}
		if _, owned := ownedAttemptName("docker.io/yoloctl/prepared/" + id + ":latest"); owned && !keep[id] {
			if err := b.Containerd.LeasesService().Delete(ctx, l); err != nil && !errdefs.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func (b *NativeImageBackend) ResolveRemote(ctx context.Context, ref string) (string, string, error) {
	name, err := canonicalImage(ref)
	if err != nil {
		return "", "", err
	}
	_, desc, err := docker.NewResolver(docker.ResolverOptions{}).Resolve(ctx, name)
	if err != nil {
		return "", "", err
	}
	return desc.Digest.String(), platforms.Format(platforms.DefaultSpec()), nil
}

func (b *NativeImageBackend) Configure(preflight func(context.Context, ImageRecipe) error, ensure func(context.Context) error) {
	b.PreflightBuild = preflight
	b.EnsureBuilder = ensure
}

// A normal returned solve error is a known failed preparation, not an
// interruption. Cancellation and unavailable/lost transport remain unknown;
// none of these classifications claims that background BuildKit work stopped.
func classifyImageSolveError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	switch grpcerrors.Code(err) {
	// Internal also covers missing trailers and HTTP/2 INTERNAL_ERROR resets;
	// neither is proof that the server returned an application outcome.
	case codes.Canceled, codes.DeadlineExceeded, codes.Unavailable, codes.DataLoss, codes.Internal:
		return err
	}
	return &ImageBuildFailure{Cause: err}
}
