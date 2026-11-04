package agentboximage

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

// ComposedImageRepo is the repository every on-demand feature-composed image is
// tagged under. Composed refs are NOT catalog entries (EnsureImage no-ops them);
// cleanup checks both this namespace and the inspected provenance label.
const ComposedImageRepo = "yoloctl/agentbox-composed"

// builderVersion salts the composed-image content hash. Bump it whenever the
// composition wiring or GenerateDockerfile/InstallPlan output changes in a way
// that should invalidate every previously-built composed image globally. A unit
// test asserts it participates in the hash.
const builderVersion = "4"

const preparedBaseStage = "yoloctl-prepared-base"

// ComposedImageRef returns the content-addressed runtime image ref for
// composing `candidates` (the features to install) onto `base`, the generated
// Dockerfile, and whether any candidate actually installs something.
//
// When hasInstall is false the caller should run `base` as-is — there is
// nothing to compose, so no image is built and the returned ref is empty.
//
// The ref is `yoloctl/agentbox-composed:<12 hex>` where the hash covers the
// builderVersion, the base ref, and the generated Dockerfile, including the
// space owner's UID/GID. The controller's root identity is never used. Policy-
// only features render no fragment, so they do not change the ref. Resolution
// is offline and deterministic within the space's immutable execution context,
// so ResolvedImage (drift) and EnsureComposedImage (build) agree.
func ComposedImageRef(base string, candidates []string, plans ...ocifeature.Plan) (ref, dockerfile string, hasInstall bool, err error) {
	uid, gid := spaceenv.Identity()
	return composedImageRef(base, candidates, uid, gid, plans...)
}

func composedImageRef(base string, candidates []string, uid, gid int, plans ...ocifeature.Plan) (ref, dockerfile string, hasInstall bool, err error) {
	// Resolve against a prepared base stage so the small identity change runs
	// before feature installers populate the sandbox home. Keep this wiring out
	// of feature definitions, including user-provided Dockerfile fragments.
	dockerfile, _, hasInstall, err = features.InstallPlan(preparedBaseStage, candidates, plans...)
	if err != nil {
		return "", "", false, err
	}
	if !hasInstall {
		return "", dockerfile, false, nil
	}
	if base == "" {
		return "", "", false, fmt.Errorf("base image is required")
	}
	dockerfile = fmt.Sprintf("FROM %s AS %s\nARG %s\n", base, preparedBaseStage, imageRefreshArg) + composedIdentitySetup(uid, gid) + "\n" + dockerfile
	// Custom installers can still write root-owned home files. Repair only
	// mismatches, without copying up correctly-owned npm/native package trees.
	dockerfile += fmt.Sprintf("\nRUN find /home/sandbox -xdev \\( ! -user sandbox -o ! -group sandbox \\) -exec chown -h sandbox:sandbox {} + \\\n"+
		"    && YOLOCTL_HOST_UID=%d YOLOCTL_HOST_GID=%d /usr/local/bin/yoloctl-entrypoint /bin/true\n", uid, gid)
	sum := sha256.Sum256([]byte(builderVersion + "\x00" + base + "\x00" + dockerfile))
	return ComposedImageRepo + ":" + hex.EncodeToString(sum[:])[:12], dockerfile, true, nil
}

// EnsureComposedImage build-or-selects the feature-composed runtime image and
// returns the ref to actually run. It is the per-create/recreate check:
//
//  1. If `candidates` install nothing (policy-only / empty) → ensure the base
//     via EnsureImage (catalog/embedded build if missing) and return base.
//  2. Else compute the composed ref. Unless opts.Force, if it is already
//     present in the VM cache, return it with NO build and NO network.
//  3. Else ensure the base is present first (the composed Dockerfile FROMs it),
//     then build the composed image, tagging it with the content-addressed ref
//     and a provenance LABEL. opts.Force reruns installers using a fresh ARG,
//     preserving download caches. Custom bases and raw Dockerfile features
//     retain full --no-cache rebuilding for arbitrary ONBUILD/stage semantics.
//
// Composed refs are non-catalog, so they cannot route through EnsureImage
// (which no-ops them); this is their dedicated build path.
func EnsureComposedImage(ctx context.Context, c Client, base string, candidates []string, opts Options) (string, error) {
	if ref, handled, err := preparedImage(ctx, c, base, candidates, opts); handled {
		return ref, err
	}
	if opts.Notice == nil {
		opts.Notice = io.Discard
	}
	ref, dockerfile, hasInstall, err := ComposedImageRef(base, candidates, opts.FeaturePlan)
	if err != nil {
		return "", err
	}
	if !hasInstall {
		// Nothing to compose: the box runs the base image as-is.
		if err := EnsureImage(ctx, c, base, opts); err != nil {
			return "", err
		}
		return base, nil
	}

	if !opts.Force {
		present, err := c.ImagePresent(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("check composed image %s: %w", ref, err)
		}
		if present {
			return ref, nil
		}
	}
	noCache, err := refreshNeedsNoCache(base, candidates, opts.Force)
	if err != nil {
		return "", err
	}

	// Build. Ensure the base (a build dependency) is present first — but never
	// Force the base here: refreshing the base is a separate operation, and the
	// composed --rebuild only re-runs the feature install layers.
	if err := EnsureImage(ctx, c, base, Options{Notice: opts.Notice}); err != nil {
		return "", err
	}
	if opts.Force {
		_, _ = fmt.Fprintf(opts.Notice, "notice: rebuilding composed image %s (cache-busted)\n", ref)
	} else {
		_, _ = fmt.Fprintf(opts.Notice,
			"notice: composed image %s not present in space; building from %s + features (this may take several minutes)\n",
			ref, base)
	}
	files, err := opts.FeaturePlan.BuildFiles(ctx)
	if err != nil {
		return "", err
	}
	tarBuf, err := TarDockerfile(dockerfile+composedLabels(base), files)
	if err != nil {
		return "", fmt.Errorf("prepare composed context for %s: %w", ref, err)
	}
	if err := c.Build(ctx, container.BuildOptions{
		Tag:         ref,
		InputImages: []string{base},
		ContextTar:  tarBuf,
		BuildArgs:   imageRefreshArgs(opts.Force),
		NoCache:     noCache,
	}); err != nil {
		return "", fmt.Errorf("build composed image %s: %w", ref, err)
	}
	return ref, nil
}

// Prepare identity before feature installs so their ownership uses the final
// UID/GID, rather than copying a populated home into another layer to remap it.
// No account or workdir mounts are present here. The entrypoint remains the
// runtime fallback for images built for a different identity, and bases used
// without install features. Its root-group/collision checks apply here too.
func composedIdentitySetup(uid, gid int) string {
	// A custom base can acquire the sandbox identity/entrypoint from a feature.
	// Preserve that path: prepare early when available, and verify again after
	// installation in the final ownership layer.
	return fmt.Sprintf("RUN if [ -x /usr/local/bin/yoloctl-entrypoint ] && id -u sandbox >/dev/null 2>&1 && getent group sandbox >/dev/null; then \\\n"+
		"    YOLOCTL_HOST_UID=%d YOLOCTL_HOST_GID=%d /usr/local/bin/yoloctl-entrypoint /bin/true; fi\n", uid, gid)
}

// Cleanup inspects these provenance labels in addition to the repository.
// They are constant for a given base and builder version, already in the hash.
func composedLabels(base string) string {
	return fmt.Sprintf(
		"\nLABEL yoloctl.kind=\"composed\" yoloctl.base=%q yoloctl.builder_version=%q\n",
		base, builderVersion)
}

// TarDockerfile packages validated OCI files alongside the build recipe.
func TarDockerfile(content string, files map[string][]byte) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "Dockerfile",
		Mode: 0o644,
		Size: int64(len(content)),
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(data))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return &buf, nil
}
