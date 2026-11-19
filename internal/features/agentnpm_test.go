package features

import (
	"strings"
	"testing"
)

const (
	managedCodexInstall = "npm install -g @openai/codex@latest --prefix=/opt/yoloctl/npm"
	homeCodexInstall    = "npm install -g @openai/codex@latest --prefix=/home/sandbox/.npm-global"
	agentPathLine       = "ENV PATH=/opt/yoloctl/npm/bin:$PATH"
)

func render(t *testing.T, names ...string) string {
	t.Helper()
	out, _, has, err := InstallPlan("base", names)
	if err != nil || !has {
		t.Fatalf("InstallPlan(%v): has=%v err=%v", names, has, err)
	}
	return out
}

// The same features put Codex in the managed prefix only where Codex is the
// box's agent (its runtime feature is present); as a tool it keeps the
// user's prefix.
func TestAgentNpmPackageGoesToTheManagedPrefixOnlyForTheAgent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tool := render(t, "codex", "github")
	agent := render(t, "codex", "github", AgentNpmRuntime("@openai/codex"))
	if !strings.Contains(tool, homeCodexInstall) || strings.Contains(tool, managedCodexInstall) || strings.Contains(tool, agentPathLine) {
		t.Fatalf("Codex as a tool left the user's prefix:\n%s", tool)
	}
	if strings.Contains(agent, homeCodexInstall) || strings.Count(agent, managedCodexInstall) != 1 {
		t.Fatalf("Codex as the agent is not installed once in the managed prefix:\n%s", agent)
	}
	for _, want := range []string{
		"USER yoloctl-maint\n",
		"useradd --system --gid yoloctl-maint --no-create-home --home-dir /var/lib/yoloctl-maint --shell /usr/sbin/nologin yoloctl-maint",
		"install -d -m 0755 -o yoloctl-maint -g yoloctl-maint /opt/yoloctl/npm",
		"cd /var/lib/yoloctl-maint && HOME=/var/lib/yoloctl-maint TMPDIR=/var/lib/yoloctl-maint " + managedCodexInstall,
		"USER root\n",
	} {
		if !strings.Contains(agent, want) {
			t.Fatalf("managed install lacks %q:\n%s", want, agent)
		}
	}
	// The codex recipe's other step stays in place.
	if !strings.Contains(agent, "--no-install-recommends bubblewrap") {
		t.Fatalf("codex recipe lost bubblewrap:\n%s", agent)
	}
}

// The agent is installed where the first feature supplying it stands, so a
// later feature can already run it; PATH puts it first again at the end.
func TestAgentNpmInstallRunsAtTheFirstSupplyingFeature(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	writeUserFeature(t, testFeaturesDir(t), "check-codex", "name: check-codex\ndescription: check\ninstall:\n  kind: dockerfile\n  dockerfile: \"RUN codex --version\"\n")
	out := render(t, "codex", "check-codex", "npm", AgentNpmRuntime("@openai/codex"))
	install := strings.Index(out, managedCodexInstall)
	firstPath := strings.Index(out, agentPathLine)
	check := strings.Index(out, "RUN codex --version")
	userPath := strings.Index(out, "ENV PATH=/home/sandbox/.npm-global/bin:$PATH")
	lastPath := strings.LastIndex(out, agentPathLine)
	ordered := install >= 0 && install < firstPath && firstPath < check && check < userPath && userPath < lastPath
	if !ordered {
		t.Fatalf("install %d, PATH %d, check %d, user PATH %d, final PATH %d:\n%s", install, firstPath, check, userPath, lastPath, out)
	}
	if !strings.HasSuffix(out, agentPathLine+"\n") {
		t.Fatalf("the agent folder is not first on the final PATH:\n%s", out)
	}
}

// A profile whose package no feature installs still gets the agent install,
// at the runtime feature's place; a second supplier installs nothing more.
func TestAgentNpmInstallIsAppendedOnceWithoutASupplier(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	alone := render(t, "github", AgentNpmRuntime("my-cli"))
	if strings.Count(alone, "npm install -g my-cli@latest --prefix=/opt/yoloctl/npm") != 1 {
		t.Fatalf("custom agent package not installed in the managed prefix:\n%s", alone)
	}
	writeUserFeature(t, testFeaturesDir(t), "my-cli-pinned", "name: my-cli-pinned\ndescription: pinned\ninstall:\n  kind: npm\n  package: my-cli\n  version: 1.2.3\n")
	writeUserFeature(t, testFeaturesDir(t), "my-cli-again", "name: my-cli-again\ndescription: again\ninstall:\n  kind: npm\n  package: my-cli\n")
	both := render(t, "my-cli-pinned", "my-cli-again", AgentNpmRuntime("my-cli"))
	if strings.Count(both, "npm install -g my-cli@") != 1 || !strings.Contains(both, "my-cli@latest --prefix=/opt/yoloctl/npm") {
		t.Fatalf("agent package not installed exactly once at latest:\n%s", both)
	}
	// As a tool the pinned feature keeps its version and the user's prefix.
	tool := render(t, "my-cli-pinned")
	if !strings.Contains(tool, "npm install -g my-cli@1.2.3 --prefix=/home/sandbox/.npm-global") || strings.Contains(tool, "/opt/yoloctl") {
		t.Fatalf("custom npm tool changed:\n%s", tool)
	}
}

func TestOpenCodeCheckFollowsItsPrefix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if out := render(t, "opencode"); !strings.Contains(out, "test -x /home/sandbox/.npm-global/lib/node_modules/opencode-ai/bin/opencode.exe") {
		t.Fatalf("OpenCode tool check:\n%s", out)
	}
	if out := render(t, "opencode", AgentNpmRuntime("opencode-ai")); !strings.Contains(out, "test -x /opt/yoloctl/npm/lib/node_modules/opencode-ai/bin/opencode.exe") {
		t.Fatalf("OpenCode agent check:\n%s", out)
	}
}

func TestAgentNpmRuntimeName(t *testing.T) {
	name := AgentNpmRuntime("@anthropic-ai/claude-code")
	if strings.Contains(name, "/") || !IsBuiltin(name) {
		t.Fatalf("runtime name %q must be a built-in without a slash", name)
	}
	f, err := Lookup(name)
	if err != nil || f.Install == nil || f.Install.Package != "@anthropic-ai/claude-code" {
		t.Fatalf("Lookup(%q) = %+v, %v", name, f.Install, err)
	}
	for _, bad := range []string{agentNpmRuntimePrefix, agentNpmRuntimePrefix + "%zz", agentNpmRuntimePrefix + "a%2fb"} {
		if _, err := Lookup(bad); err == nil || IsBuiltin(bad) {
			t.Fatalf("invalid runtime name %q accepted", bad)
		}
	}
}
