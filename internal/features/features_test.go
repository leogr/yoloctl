package features

import (
	"strings"
	"testing"
)

func TestOCIFeatureCannotSilentlySkipInstallation(t *testing.T) {
	ref := "ghcr.io/devcontainers/features/github-cli:1"
	if _, err := GenerateDockerfile("base", []string{ref}); err == nil {
		t.Fatal("rendered an OCI selection without its locked installer")
	}
	if _, _, _, err := InstallPlan("base", []string{ref}); err == nil {
		t.Fatal("treated an unresolved OCI selection as a policy-only feature")
	}
}

func TestLookup_KnownDockerfileFeature(t *testing.T) {
	f, err := Lookup("go")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if f.Name != "go" {
		t.Errorf("Name: got %q want go", f.Name)
	}
	if f.Install == nil || f.Install.Kind != InstallDockerfile || f.Install.Dockerfile == "" {
		t.Errorf("go should install via a Dockerfile fragment: %+v", f.Install)
	}
	if len(f.RequiresCapabilities) != 1 || f.RequiresCapabilities[0] != "network.go-modules" {
		t.Errorf("go RequiresCapabilities = %v, want [network.go-modules]", f.RequiresCapabilities)
	}
}

// TestLookup_AgentFeatureNpm: agent CLI installation is a feature (npm), and it
// requires its network capability — not an inline policy.
func TestLookup_AgentFeatureNpm(t *testing.T) {
	cases := []struct {
		name, pkg, capability string
	}{
		{"claude", "@anthropic-ai/claude-code", "network.anthropic"},
	}
	for _, tc := range cases {
		f, err := Lookup(tc.name)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", tc.name, err)
		}
		if f.Install == nil || f.Install.Kind != InstallNpm || f.Install.Package != tc.pkg {
			t.Errorf("%s install = %+v, want npm %s", tc.name, f.Install, tc.pkg)
		}
		if len(f.RequiresCapabilities) != 1 || f.RequiresCapabilities[0] != tc.capability {
			t.Errorf("%s RequiresCapabilities = %v, want [%s]", tc.name, f.RequiresCapabilities, tc.capability)
		}
	}
}

func TestCodexFeatureInstallsBubblewrap(t *testing.T) {
	f, err := Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.RequiresCapabilities, ","); got != "network.openai" {
		t.Fatalf("codex capabilities = %q, want only network.openai", got)
	}
	out, installing, installs, err := InstallPlan("base", []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !installs || strings.Join(installing, ",") != "codex" {
		t.Fatalf("codex installation = %v (%v), want codex", installing, installs)
	}
	for _, want := range []string{
		"update --error-on=any",
		"install -y --no-install-recommends bubblewrap",
		"rm -rf /var/lib/apt/lists/*",
		"npm install -g @openai/codex@latest --prefix=/home/sandbox/.npm-global",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing Codex dependency install %q in:\n%s", want, out)
		}
	}
}

// TestPolicyOnlyFeatureHasNoInstall: ecosystem features (github) install
// nothing — their tool is in the base image; they only inherit a capability.
func TestPolicyOnlyFeatureHasNoInstall(t *testing.T) {
	f, err := Lookup("github")
	if err != nil {
		t.Fatal(err)
	}
	if f.Install != nil {
		t.Errorf("github should be policy-only (no install), got %+v", f.Install)
	}
	if len(f.RequiresCapabilities) != 1 || f.RequiresCapabilities[0] != "network.github" {
		t.Errorf("github RequiresCapabilities = %v", f.RequiresCapabilities)
	}
}

func TestLookup_UnknownFeature(t *testing.T) {
	_, err := Lookup("nope")
	if err == nil {
		t.Fatal("expected error for unknown feature")
	}
	if !strings.Contains(err.Error(), ", go,") {
		t.Errorf("error should list known features, got: %v", err)
	}
}

func TestIntegrationNamesRequireExplicitDeclarationUpdates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, old := range []string{"yolo.op.box", "airc", "airc-server", "repartee", "yolo.chat.server", "yolo.chat.repartee"} {
		if _, err := Lookup(old); err == nil {
			t.Errorf("retired feature %q remains available", old)
		}
	}
	for _, current := range []string{"yolo.space.operator", "yolo.chat.skill"} {
		if _, err := Lookup(current); err != nil {
			t.Errorf("new feature %q unavailable: %v", current, err)
		}
	}
}

func TestNames_SortedAndNonEmpty(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("registry is empty")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("Names not sorted: %v", names)
			break
		}
	}
}

func TestPipEnvironmentIncludesPyPIWithoutChangingPolicyOnlyFeature(t *testing.T) {
	pip, err := Lookup("pip")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pip.RequiresCapabilities, ","); got != "network.pypi" {
		t.Fatalf("pip capabilities = %q, want only network.pypi", got)
	}
	_, installing, installs, err := InstallPlan("base", []string{"pip", "pypi"})
	if err != nil {
		t.Fatal(err)
	}
	if !installs || strings.Join(installing, ",") != "pip" {
		t.Fatalf("pip+pypi installation = %v (%v), want only pip", installing, installs)
	}
	_, _, installs, err = InstallPlan("base", []string{"pypi"})
	if err != nil {
		t.Fatal(err)
	}
	if installs {
		t.Fatal("pypi must remain policy-only")
	}
}

func TestGenerateDockerfile_NoFeatures(t *testing.T) {
	out, err := GenerateDockerfile("yoloctl/agentbox:ubuntu-24.04", nil)
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	if !strings.Contains(out, "FROM yoloctl/agentbox:ubuntu-24.04") {
		t.Errorf("missing FROM line: %s", out)
	}
	if strings.Contains(out, "# Feature:") {
		t.Errorf("no features should mean no Feature comments: %s", out)
	}
}

func TestGenerateDockerfile_DockerfileFeature(t *testing.T) {
	out, err := GenerateDockerfile("yoloctl/agentbox:ubuntu-24.04", []string{"go"})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	for _, want := range []string{"FROM yoloctl/agentbox:ubuntu-24.04", "# Feature: go", "go.dev/dl"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

// TestGenerateDockerfile_AgentNpmFeaturesUseLatest asserts that the built-in
// agent features track the registry's latest release instead of silently
// turning the content-addressed image tag into a CLI version pin.
func TestGenerateDockerfile_AgentNpmFeaturesUseLatest(t *testing.T) {
	for _, tc := range []struct {
		feature string
		pkg     string
	}{
		{feature: "claude", pkg: "@anthropic-ai/claude-code"},
		{feature: "codex", pkg: "@openai/codex"},
		{feature: "gemini", pkg: "@google/gemini-cli"},
		{feature: "opencode", pkg: "opencode-ai"},
		{feature: "pi", pkg: "@earendil-works/pi-coding-agent"},
	} {
		t.Run(tc.feature, func(t *testing.T) {
			out, err := GenerateDockerfile("base", []string{tc.feature})
			if err != nil {
				t.Fatalf("GenerateDockerfile: %v", err)
			}
			want := "npm install -g " + tc.pkg + "@latest --prefix=/home/sandbox/.npm-global"
			if !strings.Contains(out, want) {
				t.Errorf("missing npm install line %q in:\n%s", want, out)
			}
			// The cache is outside image layers and metadata is revalidated.
			// Ownership must be fixed before that install layer is committed.
			for _, want := range []string{
				"--mount=type=cache,id=yoloctl-npm-v1,target=/var/cache/yoloctl/npm,sharing=locked,mode=0777",
				"--cache=\"/var/cache/yoloctl/npm/$(id -u)\" --prefer-online",
				"find /home/sandbox/.npm-global -xdev \\( ! -user sandbox -o ! -group sandbox \\) -exec chown -h sandbox:sandbox {} +",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("missing npm cache/ownership contract %q in:\n%s", want, out)
				}
			}
		})
	}
}

// TestGenerateDockerfile_PolicyOnlyFeatureNoStep: a policy-only feature adds no
// build step (nothing to install).
func TestGenerateDockerfile_PolicyOnlyFeatureNoStep(t *testing.T) {
	out, err := GenerateDockerfile("base", []string{"github"})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	if strings.Contains(out, "# Feature: github") {
		t.Errorf("policy-only github should contribute no build step:\n%s", out)
	}
}

func TestGenerateDockerfile_UnknownFeatureNoPartialOutput(t *testing.T) {
	_, err := GenerateDockerfile("yoloctl/agentbox:ubuntu-24.04", []string{"go", "nope"})
	if err == nil {
		t.Fatal("expected error for unknown feature in list")
	}
}

func TestGenerateDockerfile_EmptyBase(t *testing.T) {
	_, err := GenerateDockerfile("", nil)
	if err == nil {
		t.Fatal("expected error for empty base")
	}
}

func TestGenerateDockerfile_Deterministic(t *testing.T) {
	a, _ := GenerateDockerfile("base", []string{"go", "claude"})
	b, _ := GenerateDockerfile("base", []string{"go", "claude"})
	if a != b {
		t.Error("identical inputs should produce identical Dockerfiles")
	}
}

func TestInstallPlan_InstallFeature(t *testing.T) {
	df, installing, hasInstall, err := InstallPlan("base", []string{"go"})
	if err != nil {
		t.Fatalf("InstallPlan: %v", err)
	}
	if !hasInstall || len(installing) != 1 || installing[0] != "go" {
		t.Errorf("installing=%v hasInstall=%v, want [go]/true", installing, hasInstall)
	}
	if !strings.Contains(df, "FROM base") || !strings.Contains(df, "go.dev/dl") {
		t.Errorf("dockerfile missing FROM/go install:\n%s", df)
	}
}

// A policy-only feature installs nothing → hasInstall=false (the caller then
// runs the base image as-is rather than building a pointless composed image).
func TestInstallPlan_PolicyOnlyHasNoInstall(t *testing.T) {
	_, installing, hasInstall, err := InstallPlan("base", []string{"github"})
	if err != nil {
		t.Fatalf("InstallPlan: %v", err)
	}
	if hasInstall || len(installing) != 0 {
		t.Errorf("github should install nothing: installing=%v hasInstall=%v", installing, hasInstall)
	}
}

func TestInstallPlan_MixedReportsOnlyInstallers(t *testing.T) {
	_, installing, hasInstall, err := InstallPlan("base", []string{"go", "github", "claude"})
	if err != nil {
		t.Fatalf("InstallPlan: %v", err)
	}
	if !hasInstall {
		t.Fatal("mixed set should have installs")
	}
	if got := strings.Join(installing, ","); got != "go,claude" {
		t.Errorf("installing = %q, want go,claude (github is policy-only)", got)
	}
}

func TestInstallPlan_UnknownFeatureErrors(t *testing.T) {
	if _, _, _, err := InstallPlan("base", []string{"nope"}); err == nil {
		t.Fatal("expected an error for an unknown feature")
	}
}
