package features

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// Keep the shared installer's Claude recipe and managed hook contract exact;
// the embedded writer source is checked separately.
func TestAgentObserveClaudeRecipeMatchesContract(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "agent-observe-claude.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(string(golden), "{{AGENT_OBSERVE_SOURCE}}", aircBase64Lines([]byte(agentObserveScript)), 1)
	if got := agentObserveInstallDockerfile(); got != want {
		t.Fatalf("the Claude recipe changed:\n%s", got)
	}
}

// The System-layer config holds only the hooks table, in Codex's schema
// (config/src/hook_config.rs): one command hook per kept event.
func TestAgentObserveCodexHooksTOML(t *testing.T) {
	type handler struct {
		Type    string `toml:"type"`
		Command string `toml:"command"`
		Async   bool   `toml:"async"`
		Timeout int    `toml:"timeout"`
	}
	type group struct {
		Matcher *string   `toml:"matcher"`
		Hooks   []handler `toml:"hooks"`
	}
	var cfg struct {
		Hooks map[string][]group `toml:"hooks"`
	}
	dec := toml.NewDecoder(strings.NewReader(agentObserveCodexTOML()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	var events []string
	for event, groups := range cfg.Hooks {
		events = append(events, event)
		if len(groups) != 1 || groups[0].Matcher != nil || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s: want one unmatched group with one hook, got %+v", event, groups)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != agentObserveCodexHook {
			t.Fatalf("%s: hook %+v", event, h)
		}
		// Codex runs an async SessionEnd synchronously; it is bounded instead.
		if event == "SessionEnd" {
			if h.Async || h.Timeout != 2 {
				t.Fatalf("SessionEnd must be synchronous with a 2 s timeout: %+v", h)
			}
		} else if !h.Async || h.Timeout != 0 {
			t.Fatalf("%s must be async: %+v", event, h)
		}
	}
	slices.Sort(events)
	if want := slices.Sorted(slices.Values(agentObserveCodexEvents)); !slices.Equal(events, want) {
		t.Fatalf("hook events %v, want %v", events, want)
	}
}

func TestAgentObserveCodexInstall(t *testing.T) {
	recipe, err := GenerateDockerfile("base", []string{"yolo.agent.observe-codex"})
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(recipe, "<<'AGENT_OBSERVE_INSTALL'\n")
	if !ok {
		t.Fatal("missing build script")
	}
	script, _, ok := strings.Cut(rest, "\nAGENT_OBSERVE_INSTALL\n")
	if !ok || !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Fatal("script must have a shebang so BuildKit keeps the payload out of argv")
	}
	if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("invalid installation script: %v\n%s", err, out)
	}
	if !strings.Contains(script, `"exec $python -I -S /usr/local/lib/yoloctl/agent-observe.py codex" > `+agentObserveCodexHook) ||
		!strings.Contains(script, `"exec $python -I -S /usr/local/lib/yoloctl/agent-observe.py codex-query \"\$@\"" > `+AgentObserveCodexQuery) ||
		!strings.Contains(script, "chmod 0755 "+agentObserveCodexHook+" "+AgentObserveCodexQuery+"\n") ||
		!strings.Contains(script, "echo 'yolo.agent.observe-codex needs Python 3.8+") {
		t.Fatal("the Codex launcher execs the shared writer in codex mode with the interpreter resolved at build time")
	}
	if strings.Contains(recipe, "/home/sandbox") || strings.Contains(recipe, ".codex/") || strings.Contains(recipe, "hooks.json") {
		t.Fatal("the feature must not write the shared account leaf")
	}
	// The quoted heredoc writes the config byte for byte.
	tail := script[strings.Index(script, "cat > "+agentObserveCodexConfig):]
	path := filepath.Join(t.TempDir(), "config.toml")
	if out, err := exec.Command("sh", "-c", strings.Replace(tail, agentObserveCodexConfig, path, 1)).CombinedOutput(); err != nil {
		t.Fatalf("writing the config failed: %v\n%s", err, out)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, []byte(agentObserveCodexTOML())) {
		t.Fatalf("installed config differs:\n%s", got)
	}
}

// The refresh-read launcher passes its own arguments, and only it does.
func TestAgentObserveCodexQueryLauncherForwardsArguments(t *testing.T) {
	recipe, err := GenerateDockerfile("base", []string{"yolo.agent.observe-codex"})
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(recipe, "<<'AGENT_OBSERVE_INSTALL'\n")
	script, _, _ := strings.Cut(rest, "\nAGENT_OBSERVE_INSTALL\n")
	launchers := script[strings.Index(script, "AGENT_OBSERVE_SOURCE\nprintf"):strings.Index(script, "chmod 0755")]
	launchers = strings.Replace(launchers, "AGENT_OBSERVE_SOURCE\n", "", 1)
	dir := t.TempDir()
	launchers = strings.NewReplacer(AgentObserveCodexQuery, filepath.Join(dir, "query"), agentObserveCodexHook, filepath.Join(dir, "hook")).Replace(launchers)
	if out, err := exec.Command("sh", "-c", "python=/usr/bin/python3\n"+launchers).CombinedOutput(); err != nil {
		t.Fatalf("launchers: %v\n%s", err, out)
	}
	query, err := os.ReadFile(filepath.Join(dir, "query"))
	if err != nil || string(query) != "#!/bin/sh\nexec /usr/bin/python3 -I -S /usr/local/lib/yoloctl/agent-observe.py codex-query \"$@\"\n" {
		t.Fatalf("query launcher = %q (%v)", query, err)
	}
	hook, err := os.ReadFile(filepath.Join(dir, "hook"))
	if err != nil || strings.Contains(string(hook), "$@") {
		t.Fatalf("hook launcher = %q (%v)", hook, err)
	}
}
