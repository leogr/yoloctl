package agentboximage

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/imagejob"
)

// PreparedContext separates the desired recipe name used for drift/provenance
// from the immutable bytes consumed by a controller-owned build. Preparation
// copies every context file and performs no image build, publication or GC.
type PreparedContext struct {
	DesiredRef string
	BaseRef    string
	Recipe     imagejob.Recipe
	// Features names the features a composed image installs, for progress
	// only; the recipe alone defines the build.
	Features []string
}

// BindBase fixes the platform and the immutable FROM image at prepare time.
// The backend must require that digest, independently of any later tag move.
func (p PreparedContext) BindBase(digest, platform string) (imagejob.Recipe, error) {
	r := imagejob.Copy(p.Recipe)
	r.BaseDigest, r.Platform = digest, platform
	if _, err := r.Identity(); err != nil {
		return imagejob.Recipe{}, err
	}
	return r, nil
}

// PrepareComposed produces the exact desired recipe, including OCI Feature
// BuildFiles. A base-only result has no Dockerfile: its caller may select a
// cached base, prepare the managed base, or use the named locked cold path.
func PrepareComposed(ctx context.Context, base string, candidates []string, opts Options) (PreparedContext, error) {
	ref, dockerfile, install, err := ComposedImageRef(base, candidates, opts.FeaturePlan)
	if err != nil {
		return PreparedContext{}, err
	}
	if !install {
		return PreparedContext{DesiredRef: base, BaseRef: base}, nil
	}
	files, err := opts.FeaturePlan.BuildFiles(ctx)
	if err != nil {
		return PreparedContext{}, err
	}
	noCache, err := refreshNeedsNoCache(base, candidates, opts.Force)
	if err != nil {
		return PreparedContext{}, err
	}
	r := imagejob.Recipe{Builder: "composed/" + builderVersion, Dockerfile: []byte(dockerfile + composedLabels(base)), Files: files, Force: opts.Force, NoCache: noCache}
	return PreparedContext{DesiredRef: ref, BaseRef: base, Recipe: imagejob.Copy(r), Features: slices.Sorted(slices.Values(candidates))}, nil
}

func refreshNeedsNoCache(base string, candidates []string, force bool) (bool, error) {
	if !force {
		return false, nil
	}
	if base != RefAgentbox {
		return true, nil
	}
	for _, name := range candidates {
		f, err := features.Lookup(name)
		if err != nil {
			return false, err
		}
		if !features.IsBuiltin(name) && f.Install != nil && f.Install.Kind == features.InstallDockerfile {
			return true, nil
		}
	}
	return false, nil
}

// PrepareManagedBase snapshots the embedded context. Its external FROM must be
// resolved by the controller before this recipe can acquire a build permit.
func PrepareManagedBase(force bool) (PreparedContext, error) {
	files := map[string][]byte{}
	modes := map[string]uint32{}
	if err := fs.WalkDir(assets.AgentboxContexts, agentboxContext, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := relPath(agentboxContext, name)
		if err != nil {
			return err
		}
		raw, err := fs.ReadFile(assets.AgentboxContexts, name)
		if err != nil {
			return err
		}
		files[rel] = raw
		modes[rel] = 0644
		return nil
	}); err != nil {
		return PreparedContext{}, err
	}
	dockerfile, ok := files["Dockerfile"]
	if !ok {
		return PreparedContext{}, fmt.Errorf("managed base has no Dockerfile")
	}
	delete(files, "Dockerfile")
	delete(modes, "Dockerfile")
	// This checked recipe has exactly one external stage. Refuse a future
	// multi-stage base until its immutable dependency graph is implemented.
	base := ""
	for _, line := range strings.Split(string(dockerfile), "\n") {
		words := strings.Fields(line)
		if len(words) == 0 || !strings.EqualFold(words[0], "FROM") {
			continue
		}
		if len(words) != 2 || base != "" {
			return PreparedContext{}, fmt.Errorf("managed base requires one plain FROM dependency")
		}
		base = words[1]
	}
	if base == "" {
		return PreparedContext{}, fmt.Errorf("managed base lacks a FROM dependency")
	}
	return PreparedContext{DesiredRef: RefAgentbox, BaseRef: base, Recipe: imagejob.Recipe{Builder: "managed-base/1", Dockerfile: dockerfile, Files: files, FileModes: modes, Force: force}}, nil
}
