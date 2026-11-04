package agentboximage

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/nativerelease"
)

func TestAgentboxContextExists(t *testing.T) {
	if _, err := assets.AgentboxContexts.ReadFile(agentboxContext + "/Dockerfile"); err != nil {
		t.Fatalf("base image context %q: %v", agentboxContext, err)
	}
}

// TestTarContext_AgentboxLayout asserts the tar emitted for the
// agentbox context contains the two known files at the tar root
// (no "containers/agentbox/" prefix), which is the layout the
// VM-side `tar -xf -` + `nerdctl build .` pair expects.
func TestTarContext_AgentboxLayout(t *testing.T) {
	buf, err := tarContext(assets.AgentboxContexts, "containers/agentbox")
	if err != nil {
		t.Fatalf("tarContext: %v", err)
	}
	got := map[string][]byte{}
	tr := tar.NewReader(buf)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if strings.HasPrefix(h.Name, "containers/") {
			t.Errorf("tar entry %q still has containers/ prefix; should be root-relative", h.Name)
		}
		body := make([]byte, h.Size)
		if _, err := io.ReadFull(tr, body); err != nil {
			t.Fatalf("read body %s: %v", h.Name, err)
		}
		got[h.Name] = body
	}
	if _, ok := got["Dockerfile"]; !ok {
		t.Errorf("Dockerfile missing from tar; got keys: %v", keys(got))
	}
	if _, ok := got["yoloctl-entrypoint.sh"]; !ok {
		t.Errorf("yoloctl-entrypoint.sh missing from tar; got keys: %v", keys(got))
	}
	// Sanity-check the Dockerfile contents actually round-tripped
	// through the embed → tar path (catches a regression where a
	// future refactor swaps to a zero-byte placeholder).
	if d, ok := got["Dockerfile"]; ok {
		if !bytes.Contains(d, []byte("FROM ubuntu:24.04")) {
			t.Errorf("Dockerfile content unexpected: missing FROM line; first 80 bytes: %q",
				d[:min(80, len(d))])
		}
	}
}

// fakeClient is the minimal stub of Client for ensureImage tests.
// imagesPresent maps refs that ImagePresent should report as present;
// every Build call appends its relevant options to builds.
type fakeClient struct {
	imagesPresent map[string]bool
	builds        []fakeBuild
	buildErr      error
	inspectErr    error
}

type fakeBuild struct {
	Tag        string
	NoCache    bool
	BuildArgs  map[string]string
	Dockerfile string
}

func (f *fakeClient) ImagePresent(_ context.Context, ref string) (bool, error) {
	if f.inspectErr != nil {
		return false, f.inspectErr
	}
	return f.imagesPresent[ref], nil
}

func (f *fakeClient) Build(_ context.Context, opts container.BuildOptions) error {
	var dockerfile string
	if opts.ContextTar != nil {
		tr := tar.NewReader(opts.ContextTar)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if h.Name == "Dockerfile" {
				raw, err := io.ReadAll(tr)
				if err != nil {
					return err
				}
				dockerfile = string(raw)
			}
		}
	}
	if f.buildErr != nil {
		return f.buildErr
	}
	f.builds = append(f.builds, fakeBuild{Tag: opts.Tag, NoCache: opts.NoCache, BuildArgs: opts.BuildArgs, Dockerfile: dockerfile})
	if f.imagesPresent == nil {
		f.imagesPresent = map[string]bool{}
	}
	f.imagesPresent[opts.Tag] = true
	return nil
}

func (f *fakeClient) builtTags() []string {
	out := make([]string, 0, len(f.builds))
	for _, b := range f.builds {
		out = append(out, b.Tag)
	}
	return out
}

// TestProfileNpmPackageMatchesFeature asserts every built-in agent profile's
// npm_package — the package `yoloctl agent update` / the runtime autoupdate
// refreshes — matches the npm package its CLI FEATURE installs into the
// feature-composed image. Drift would mean the composed image bakes one CLI but
// autoupdate refreshes a different one (the v2.x-claude class of silent bug).
//
// The build source of truth is the feature's Install, while the runtime-update
// source of truth is the profile's npm_package. The two agreeing is the
// invariant.
func TestProfileNpmPackageMatchesFeature(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles, err := agentprofile.All()
	if err != nil {
		t.Fatalf("agentprofile.All: %v", err)
	}
	for _, p := range profiles {
		if p.NpmPackage == "" {
			continue
		}
		// Inspect the actual recipe: a Dockerfile installer may pair npm with
		// image-owned configuration (Gemini), just as an npm installer may
		// supply only the package. Both must install the update package.
		recipe, err := features.GenerateDockerfile(p.Image, p.RequiresFeatures)
		if err != nil {
			t.Fatalf("profile %q install recipe: %v", p.Name, err)
		}
		want := "npm install -g " + p.NpmPackage + "@latest --prefix=" + features.NPMGlobalPrefix + " "
		if !strings.Contains(recipe, want) {
			t.Errorf("profile %q npm_package %q has no matching latest npm install in requires_features %v; "+
				"the composed image and autoupdate would install different packages",
				p.Name, p.NpmPackage, p.RequiresFeatures)
		}
	}
}

// TestEnsureImage_UnmanagedRefIsNoop: refs other than the managed base must
// flow straight through without calling Build or ImagePresent —
// they're deferred to nerdctl's pull semantics.
func TestEnsureImage_UnmanagedRefIsNoop(t *testing.T) {
	f := &fakeClient{}
	if err := EnsureImage(context.Background(), f, "ghcr.io/some/other:v1", Options{}); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}
	if len(f.builds) != 0 {
		t.Errorf("unmanaged ref triggered builds: %v", f.builtTags())
	}
}

// TestEnsureImage_PresentIsNoop: the managed base already present in
// the cache must NOT trigger a build.
func TestEnsureImage_PresentIsNoop(t *testing.T) {
	f := &fakeClient{imagesPresent: map[string]bool{RefAgentbox: true}}
	if err := EnsureImage(context.Background(), f, RefAgentbox, Options{}); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}
	if len(f.builds) != 0 {
		t.Errorf("present ref triggered builds: %v", f.builtTags())
	}
}

func TestEnsureImage_BuildsMissingBase(t *testing.T) {
	f := &fakeClient{imagesPresent: map[string]bool{}}
	notice := &bytes.Buffer{}
	if err := EnsureImage(context.Background(), f, RefAgentbox, Options{Notice: notice}); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}
	if got := f.builtTags(); len(got) != 1 || got[0] != RefAgentbox {
		t.Errorf("built = %v, want [%s]", got, RefAgentbox)
	}
	if !strings.Contains(notice.String(), RefAgentbox) {
		t.Errorf("notice missing %q; got: %q", RefAgentbox, notice.String())
	}
}

// TestEnsureImage_BuildErrorPropagates: a failure on Build surfaces
// to the caller.
func TestEnsureImage_BuildErrorPropagates(t *testing.T) {
	f := &fakeClient{
		imagesPresent: map[string]bool{},
		buildErr:      errors.New("nerdctl exit 1"),
	}
	err := EnsureImage(context.Background(), f, RefAgentbox, Options{})
	if err == nil {
		t.Fatalf("EnsureImage: want error, got nil")
	}
	if !strings.Contains(err.Error(), "nerdctl exit 1") {
		t.Errorf("error %v doesn't wrap underlying cause", err)
	}
}

// TestEnsureImage_NilNoticeOK: passing nil Notice must not panic;
// it's the same as io.Discard.
func TestEnsureImage_NilNoticeOK(t *testing.T) {
	f := &fakeClient{imagesPresent: map[string]bool{RefAgentbox: true}}
	if err := EnsureImage(context.Background(), f, RefAgentbox, Options{}); err != nil {
		t.Fatalf("EnsureImage(nil notice): %v", err)
	}
}

// Force must rerun installers on every call without discarding their downloaded
// packages. Removing the tag alone is insufficient: BuildKit still has layers.
func TestEnsureImage_ForceRebuildsPresent(t *testing.T) {
	f := &fakeClient{imagesPresent: map[string]bool{RefAgentbox: true}}
	for range 2 {
		if err := EnsureImage(context.Background(), f, RefAgentbox, Options{Force: true}); err != nil {
			t.Fatalf("EnsureImage(Force): %v", err)
		}
	}
	assertInstallerRefresh(t, f.builds, RefAgentbox)
}

func assertInstallerRefresh(t *testing.T, builds []fakeBuild, ref string) {
	t.Helper()
	if len(builds) != 2 {
		t.Fatalf("refresh builds = %d, want two", len(builds))
	}
	for _, b := range builds {
		if b.Tag != ref || b.NoCache || b.BuildArgs[imageRefreshArg] == "" {
			t.Fatalf("refresh must replace the same tag and retain downloads: tag=%q, noCache=%v, args=%v", b.Tag, b.NoCache, b.BuildArgs)
		}
		arg := strings.Index(b.Dockerfile, "\nARG "+imageRefreshArg+"\n")
		run := strings.Index(b.Dockerfile, "\nRUN ")
		if arg < 0 || run <= arg || strings.Contains(b.Dockerfile, "ENV "+imageRefreshArg) {
			t.Fatal("refresh ARG must precede installers without persisting runtime ENV")
		}
	}
	if builds[0].BuildArgs[imageRefreshArg] == builds[1].BuildArgs[imageRefreshArg] {
		t.Fatal("repeated refresh would reuse the previous install layers")
	}
	if builds[0].Dockerfile != builds[1].Dockerfile {
		t.Fatal("refresh must change build arguments, not the stable recipe")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestProfileNativeReleaseMatchesFeature(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles, err := agentprofile.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.NativeRelease == "" {
			continue
		}
		release, err := nativerelease.Lookup(p.NativeRelease)
		if err != nil {
			t.Fatal(err)
		}
		recipe, err := features.GenerateDockerfile(p.Image, p.RequiresFeatures)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(recipe, release.Dockerfile()) {
			t.Fatalf("profile %s installs a different native distribution from its updater", p.Name)
		}
	}
}
