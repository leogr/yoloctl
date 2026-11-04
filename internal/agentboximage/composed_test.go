package agentboximage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestComposedImageRef_PolicyOnlyHasNoInstall(t *testing.T) {
	ref, _, hasInstall, err := ComposedImageRef(RefAgentbox, []string{"github"})
	if err != nil {
		t.Fatal(err)
	}
	if hasInstall || ref != "" {
		t.Errorf("policy-only set: ref=%q hasInstall=%v, want empty/false", ref, hasInstall)
	}
}

func TestComposedImageRef_InstallFeatureProducesRef(t *testing.T) {
	ref, df, hasInstall, err := ComposedImageRef(RefAgentbox, []string{"go"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasInstall {
		t.Fatal("go should install")
	}
	if !strings.HasPrefix(ref, ComposedImageRepo+":") {
		t.Errorf("ref = %q, want %s:<hash>", ref, ComposedImageRepo)
	}
	if !strings.Contains(df, "FROM "+RefAgentbox) || !strings.Contains(df, "go.dev/dl") {
		t.Errorf("dockerfile missing FROM/go install:\n%s", df)
	}
}

func TestComposedImageRef_Deterministic(t *testing.T) {
	a, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go"})
	b, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go"})
	if a != b {
		t.Errorf("non-deterministic: %q vs %q", a, b)
	}
}

func TestComposedImageRef_IdentitySeparatesCache(t *testing.T) {
	refs := make(map[string]bool)
	for _, identity := range [][2]int{{1000, 985}, {1001, 985}, {1000, 20}, {501, 20}} {
		uid, gid := identity[0], identity[1]
		ref, recipe, installs, err := composedImageRef(RefAgentbox, []string{"pi"}, uid, gid)
		if err != nil || !installs || refs[ref] {
			t.Fatalf("identity %v reused an incompatible image: %q, %v", identity, ref, err)
		}
		refs[ref] = true
		want := fmt.Sprintf("YOLOCTL_HOST_UID=%d YOLOCTL_HOST_GID=%d /usr/local/bin/yoloctl-entrypoint /bin/true", uid, gid)
		if !strings.Contains(recipe, want) || strings.Index(recipe, want) > strings.Index(recipe, "npm install -g") {
			t.Fatalf("identity %v was not applied before installing tools:\n%s", identity, recipe)
		}
		// A final repair must leave already-owned files alone, while retaining
		// the identity fallback for custom fragments. Identity must never leak
		// into the container ENV when this image is reused by another space.
		setup := recipe[strings.LastIndex(recipe, "\nRUN "):]
		if strings.Contains(recipe, "chown -R") || !strings.Contains(setup, "find /home/sandbox -xdev \\( ! -user sandbox -o ! -group sandbox \\)") || !strings.Contains(setup, want) || strings.Contains(recipe, "ENV YOLOCTL_HOST_") {
			t.Fatalf("identity setup recopies owned files or persists env:\n%s", recipe)
		}
		withPolicy, _, _, err := composedImageRef(RefAgentbox, []string{"pi", "github"}, uid, gid)
		if err != nil || ref != withPolicy {
			t.Fatalf("policy-only feature changed identity cache: %q, %q, %v", ref, withPolicy, err)
		}
		if base, _, installs, err := composedImageRef(RefAgentbox, []string{"github"}, uid, gid); err != nil || installs || base != "" {
			t.Fatalf("policy-only box unexpectedly composed an identity image: %q, %v", base, err)
		}
	}
}

func TestComposedImageRef_InstallRequiresBase(t *testing.T) {
	if _, _, _, err := composedImageRef("", []string{"codex"}, 1000, 1000); err == nil {
		t.Fatal("preparation stage must not hide an empty base image")
	}
}

// A custom feature may install the identity or entrypoint itself. Execute the
// preparation shell with fixtures so early setup cannot break that ordering,
// and cannot mutate the test host's real account database.
func TestComposedPreparationDefersMissingIdentity(t *testing.T) {
	for _, available := range []bool{false, true} {
		t.Run(fmt.Sprintf("identity=%v", available), func(t *testing.T) {
			dir := t.TempDir()
			lookup := "#!/bin/sh\nexit 1\n"
			if available {
				lookup = "#!/bin/sh\nexit 0\n"
			}
			for _, name := range []string{"id", "getent"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(lookup), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			entrypoint := filepath.Join(dir, "entrypoint")
			if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\nprintf '%s:%s' \"$YOLOCTL_HOST_UID\" \"$YOLOCTL_HOST_GID\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_ENTRYPOINT", entrypoint)
			script := strings.ReplaceAll(strings.TrimPrefix(composedIdentitySetup(501, 20), "RUN "), "/usr/local/bin/yoloctl-entrypoint", `"$TEST_ENTRYPOINT"`)
			out, err := exec.Command("sh", "-c", script).CombinedOutput()
			want := ""
			if available {
				want = "501:20"
			}
			if err != nil || string(out) != want {
				t.Fatalf("preparation = %q, %v; want %q", out, err, want)
			}
			if err := os.Remove(entrypoint); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil || len(out) != 0 {
				t.Fatalf("missing entrypoint must defer setup: %q, %v", out, err)
			}
		})
	}
}

func TestComposedImageRef_UsesSpaceOwner(t *testing.T) {
	// Configure is deliberately process-global and immutable. Run in a child
	// with a space owner distinct from the worker's actual numeric identity.
	if os.Getenv("YOLOCTL_TEST_COMPOSE_IDENTITY") != "1" {
		t.Setenv("YOLOCTL_TEST_COMPOSE_IDENTITY", "1")
		cmd := exec.Command(os.Args[0], "-test.run=^TestComposedImageRef_UsesSpaceOwner$")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native image plan: %v\n%s", err, out)
		}
		return
	}
	uid, gid := os.Getuid()+123, os.Getgid()+456
	if err := spaceenv.Configure(spaceenv.Context{
		Schema: 1, Space: "work", DiskID: strings.Repeat("a", 32), DiskUUID: "uuid",
		DiskMount: "/mnt/native", ConfigDir: "/mnt/native/config", CatalogDir: spaceenv.CatalogRoot,
		HostConfigRoot: "/home/developer/.config/yoloctl", UID: uid, GID: gid,
		Bundle: "bundle", Generation: "generation",
	}); err != nil {
		t.Fatal(err)
	}
	wantRef, wantRecipe, _, err := composedImageRef(RefAgentbox, []string{"pi"}, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeClient{imagesPresent: map[string]bool{RefAgentbox: true}}
	ref, err := EnsureComposedImage(t.Context(), f, RefAgentbox, []string{"pi"}, Options{})
	if err != nil || ref != wantRef || len(f.builds) != 1 {
		t.Fatalf("build selected the worker identity instead of the space owner: %q, %v", ref, err)
	}
	if f.builds[0].Dockerfile != wantRecipe+composedLabels(RefAgentbox) {
		t.Fatalf("built recipe differs from desired recipe:\n%s", f.builds[0].Dockerfile)
	}
}

// A policy-only feature renders no Dockerfile step, so adding it must NOT move
// the composed ref (no image effect → no spurious rebuild/recreate).
func TestComposedImageRef_PolicyOnlyDoesNotChangeRef(t *testing.T) {
	withGo, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go"})
	withGoGithub, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go", "github"})
	if withGo != withGoGithub {
		t.Errorf("policy-only feature changed the ref: %q vs %q", withGo, withGoGithub)
	}
}

// npm used to be policy-only. Its user configuration now needs a composed
// image, including when added to a box which already installs an agent.
func TestComposedImageRef_NpmEnvironmentChangesImage(t *testing.T) {
	for _, existing := range [][]string{nil, {"codex"}} {
		before, _, _, err := ComposedImageRef(RefAgentbox, existing)
		if err != nil {
			t.Fatal(err)
		}
		withNpm := append(append([]string(nil), existing...), "npm")
		after, _, installs, err := ComposedImageRef(RefAgentbox, withNpm)
		if err != nil {
			t.Fatal(err)
		}
		if !installs || after == "" || after == before {
			t.Fatalf("adding npm to %v must change the runtime image: %q -> %q (installs=%v)", existing, before, after, installs)
		}
	}
}

func TestComposedImageRef_SensitiveToFeaturesAndBase(t *testing.T) {
	goRef, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go"})
	cargoRef, _, _, _ := ComposedImageRef(RefAgentbox, []string{"cargo"})
	if goRef == cargoRef {
		t.Error("different install features should yield different refs")
	}
	otherBase, _, _, _ := ComposedImageRef("ubuntu:24.04", []string{"go"})
	if goRef == otherBase {
		t.Error("different base should yield a different ref")
	}
}

// pip adds runtime setup even when PyPI access already exists. The policy-only
// pypi feature must remain redundant once pip is selected.
func TestComposedImageRef_PipEnvironmentChangesImage(t *testing.T) {
	for _, existing := range [][]string{{"pypi"}, {"codex", "pypi"}, {"python-3.12", "pypi"}} {
		before, _, _, err := ComposedImageRef(RefAgentbox, existing)
		if err != nil {
			t.Fatal(err)
		}
		next := append(append([]string(nil), existing...), "pip")
		after, _, installs, err := ComposedImageRef(RefAgentbox, next)
		if err != nil {
			t.Fatal(err)
		}
		if !installs || after == "" || after == before {
			t.Fatalf("adding pip to %v must change the runtime image: %q -> %q (installs=%v)", existing, before, after, installs)
		}
	}
	pipRef, _, _, err := ComposedImageRef(RefAgentbox, []string{"pip"})
	if err != nil {
		t.Fatal(err)
	}
	bothRef, _, _, err := ComposedImageRef(RefAgentbox, []string{"pip", "pypi"})
	if err != nil {
		t.Fatal(err)
	}
	if pipRef != bothRef {
		t.Fatal("adding policy-only pypi to pip changed the composed image")
	}
}

func TestEnsureComposedImage_PolicyOnlyReturnsBase(t *testing.T) {
	f := &fakeClient{imagesPresent: map[string]bool{RefAgentbox: true}}
	ref, err := EnsureComposedImage(context.Background(), f, RefAgentbox, []string{"github"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ref != RefAgentbox {
		t.Errorf("ref = %q, want base %q", ref, RefAgentbox)
	}
	if len(f.builds) != 0 {
		t.Errorf("policy-only must not build a composed image, got %v", f.builtTags())
	}
}

func TestEnsureComposedImage_SelectsWhenPresent(t *testing.T) {
	expRef, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go"})
	f := &fakeClient{imagesPresent: map[string]bool{RefAgentbox: true, expRef: true}}
	ref, err := EnsureComposedImage(context.Background(), f, RefAgentbox, []string{"go"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ref != expRef {
		t.Errorf("ref = %q, want %q", ref, expRef)
	}
	if len(f.builds) != 0 {
		t.Errorf("a present composed image must not rebuild, got %v", f.builtTags())
	}
}

func TestEnsureComposedImage_BuildsWhenAbsent(t *testing.T) {
	expRef, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go"})
	// Base present, composed absent: only the composed image builds.
	f := &fakeClient{imagesPresent: map[string]bool{RefAgentbox: true}}
	ref, err := EnsureComposedImage(context.Background(), f, RefAgentbox, []string{"go"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ref != expRef {
		t.Errorf("ref = %q, want %q", ref, expRef)
	}
	if got := f.builtTags(); len(got) != 1 || got[0] != expRef {
		t.Errorf("expected one build of %q, got %v", expRef, got)
	}
}

// Cold path: base also missing → base builds first (from the embedded context),
// then the composed image on top.
func TestEnsureComposedImage_BuildsBaseThenComposed(t *testing.T) {
	expRef, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go"})
	f := &fakeClient{}
	ref, err := EnsureComposedImage(context.Background(), f, RefAgentbox, []string{"go"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ref != expRef {
		t.Errorf("ref = %q, want %q", ref, expRef)
	}
	if got := f.builtTags(); len(got) != 2 || got[0] != RefAgentbox || got[1] != expRef {
		t.Errorf("expected [base, composed], got %v", got)
	}
}

func TestEnsureComposedImage_ForceRetainsDownloadCaches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	expRef, _, _, _ := ComposedImageRef(RefAgentbox, []string{"go"})
	f := &fakeClient{imagesPresent: map[string]bool{RefAgentbox: true, expRef: true}}
	for range 2 {
		ref, err := EnsureComposedImage(context.Background(), f, RefAgentbox, []string{"go"}, Options{Force: true})
		if err != nil || ref != expRef {
			t.Fatalf("refresh ref = %q, error = %v, want %q", ref, err, expRef)
		}
	}
	assertInstallerRefresh(t, f.builds, expRef)
	// An ordinary create after refresh must select the same tag without a
	// third build, even though the last build received a unique refresh ARG.
	ref, err := EnsureComposedImage(context.Background(), f, RefAgentbox, []string{"go"}, Options{})
	if err != nil || ref != expRef || len(f.builds) != 2 {
		t.Fatalf("ordinary create missed refreshed cache: ref=%q, err=%v, builds=%v", ref, err, f.builtTags())
	}
}

func TestEnsureComposedImage_CustomRefresh(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Independent stages do not inherit the prepared stage's ARG. Never
	// silently reuse their RUN results on an explicit rebuild.
	raw := "FROM ubuntu:24.04 AS custom-stage\nRUN touch /fresh\nFROM " + RefAgentbox + "\nCOPY --from=custom-stage /fresh /fresh\n"
	for _, f := range []features.Feature{
		{Name: "custom-stages", Install: &features.Install{Kind: features.InstallDockerfile, Dockerfile: raw}},
		{Name: "custom-npm", Install: &features.Install{Kind: features.InstallNpm, Package: "example", Version: "latest"}},
		{Name: "custom-policy"},
	} {
		if err := features.SaveUser(f); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, base string
		noCache    bool
	}{
		{"custom-stages", RefAgentbox, true},
		{"custom-npm", RefAgentbox, false},
		{"custom-policy", RefAgentbox, false},
		{"claude", "example/custom-base:latest", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeClient{imagesPresent: map[string]bool{tc.base: true}}
			_, err := EnsureComposedImage(context.Background(), f, tc.base, []string{"go", tc.name}, Options{Force: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(f.builds) != 1 || f.builds[0].NoCache != tc.noCache {
				t.Fatalf("custom refresh strategy: builds=%v, want noCache=%v", f.builtTags(), tc.noCache)
			}
			if tc.name == "custom-stages" && !strings.Contains(f.builds[0].Dockerfile, raw) {
				t.Fatal("custom Dockerfile fragment was rewritten")
			}
		})
	}
}

// Managed installers rely on inheriting the prepared stage's refresh ARG.
// A future builtin introducing an unrelated stage needs a refresh strategy too.
func TestBuiltinInstallersPreservePreparedStage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range features.BuiltinNames() {
		f, err := features.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if f.Install == nil {
			continue
		}
		for _, line := range strings.Split(f.Install.Dockerfile, "\n") {
			words := strings.Fields(line)
			if len(words) > 0 && strings.EqualFold(words[0], "FROM") {
				t.Errorf("builtin %s introduces a stage without inheriting the refresh ARG", name)
			}
		}
		if strings.Contains(f.Install.Dockerfile, imageRefreshArg) {
			t.Errorf("builtin %s overrides the image builder's refresh ARG", name)
		}
	}
}
