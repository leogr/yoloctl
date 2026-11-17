package features

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestAgentmonInstallsOneVerifiedCommit(t *testing.T) {
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(agentmonRevision) {
		t.Fatal("the source must be one full commit")
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(agentmonSHA256) {
		t.Fatal("the source archive needs its SHA-256")
	}
	f, err := Lookup("agentmon")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.RequiresCapabilities) != 0 {
		t.Fatal("installing AgentMon must grant no network or filesystem access")
	}
	recipe, err := GenerateDockerfile("base", []string{"agentmon"})
	if err != nil {
		t.Fatal(err)
	}
	verify := strings.Index(recipe, "sha256sum -c -")
	unpack := strings.Index(recipe, "tar -xzf")
	if verify < 0 || unpack < verify {
		t.Fatal("the archive must be verified before it is unpacked")
	}
	for _, want := range []string{
		"https://github.com/codenotary/agentmon-live/archive/" + agentmonRevision + ".tar.gz",
		agentmonSHA256,
		"--proto '=https'",
		"test -f /usr/local/share/agentmon-live/.claude-plugin/plugin.json",
		"> /etc/claude-code/managed-settings.d/60-agentmon.json",
	} {
		if !strings.Contains(recipe, want) {
			t.Errorf("recipe lacks %q", want)
		}
	}
}

func TestAgentmonSettingsKeepAIRCPlugin(t *testing.T) {
	var settings map[string]map[string]string
	if err := json.Unmarshal([]byte(agentmonSettingsJSON()), &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings) != 1 || len(settings["env"]) != 1 {
		t.Fatalf("the drop-in sets one environment value only: %v", settings)
	}
	if got := settings["env"]["CLAUDE_CODE_PLUGIN_DIRS"]; got != aircClaudePlugin+":/usr/local/share/agentmon-live" {
		t.Fatalf("plugin directories = %q", got)
	}
	// The later drop-in decides the shared value, so it must sort after AIRC's.
	if agentmonSettings <= aircClaudeSettings {
		t.Fatal("the AgentMon drop-in must sort after AIRC's")
	}
}
