package features

import (
	"bufio"
	"encoding/json"
	"maps"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestAIRCReviewedSourceAndPackaging(t *testing.T) {
	manifest := aircReleaseManifest()
	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(manifest.Revision) {
		t.Fatal("release must name its full source commit")
	}
	// The published v0.1.0 release (source 11c90677, which contains the
	// reviewed TLS verifier); hashes match its SHA256SUMS asset.
	want := map[string]aircAsset{
		"amd64": {"https://github.com/leogr/airc/releases/download/v0.1.0/airc-linux-amd64.tar.gz", "5e9f08f090fb7d8324081a4322db51de0e09d37957f16d2d97e57b6bf6aa52ec"},
		"arm64": {"https://github.com/leogr/airc/releases/download/v0.1.0/airc-linux-arm64.tar.gz", "3912c3c5456eaadd6b08c8605e5ce4640c93f883ae52285a8253fbe6f9bf5410"},
	}
	if manifest.Release != "v0.1.0" || manifest.Revision != "11c90677be7976b6856358218c0865231a22d5a9" || !maps.Equal(manifest.Assets, want) {
		t.Fatalf("AIRC must install the published v0.1.0 release assets: %+v", manifest)
	}
	for _, name := range []string{"yolo.chat.skill"} {
		f, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.RequiresCapabilities) != 0 {
			t.Fatal("installing AIRC must not grant network or operator authority")
		}
		recipe, err := GenerateDockerfile("base", []string{name, "codex"})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{want["amd64"].URL, want["amd64"].SHA256, want["arm64"].URL, want["arm64"].SHA256, "x86_64) AIRC_URL=", "aarch64) AIRC_URL=", "/tmp/yoloctl-airc/release/bin/airc-codex", "/etc/codex/skills/airc", "/etc/claude-code/.claude/skills/airc", "/home/sandbox/.agents/skills/airc", "@openai/codex@latest", "cp -R /tmp/yoloctl-airc/release/integrations/claude-plugin " + aircClaudePlugin, aircClaudeSettings} {
			if !strings.Contains(recipe, want) {
				t.Errorf("%s missing %q", name, want)
			}
		}
		for _, want := range []string{
			"curl -fsSL --proto '=https' --tlsv1.2",
			"if [ \"$(cat /usr/local/share/airc/revision 2>/dev/null)\" != '" + manifest.Revision + "' ]; then",
			"printf '%s\\n' '" + manifest.Revision + "' > /usr/local/share/airc/revision",
		} {
			if !strings.Contains(recipe, want) {
				t.Errorf("source verification/revision guard missing %q", want)
			}
		}
		for _, forbidden := range []string{"cargo", "codeload.github.com", "--mount=from="} {
			if strings.Contains(recipe, forbidden) {
				t.Errorf("release install must not build from source (%q)", forbidden)
			}
		}
		if lower := strings.ToLower(recipe); strings.Contains(lower, "repartee") || strings.Contains(lower, "airc-box") {
			t.Fatal("the AIRC tools must not package the removed human client or its launcher")
		}
	}
}

// Installing the client/recipe must not start an app server, subscriber or
// select a new delivery route for an existing Codex session.
func TestAIRCCodexDeliveryRemainsOptIn(t *testing.T) {
	recipe, err := GenerateDockerfile("base", []string{"yolo.chat.skill", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"airc subscribe", "--app-server-socket", "AIRC_CODEX_SOCKET="} {
		if strings.Contains(recipe, forbidden) {
			t.Fatalf("client installation activates delivery with %q", forbidden)
		}
	}
	if !strings.Contains(recipe, "cp -R /tmp/yoloctl-airc/release/skills/airc /usr/local/share/airc/skill") {
		t.Fatal("the release's delivery/recovery recipe must be installed recursively")
	}
}

func TestAIRCClaudeDeliverySettings(t *testing.T) {
	var settings struct {
		Env map[string]string `json:"env"`
	}
	decoder := json.NewDecoder(strings.NewReader(aircClaudeSettingsJSON()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		t.Fatal(err)
	}
	// Workers get the default mentions wake; a coordinator sets
	// AIRC_DELIVERY_WAKE in its own box env.
	want := map[string]string{"CLAUDE_CODE_PLUGIN_DIRS": aircClaudePlugin, "AIRC_DELIVERY_SESSION": "space"}
	if !maps.Equal(settings.Env, want) {
		t.Fatalf("managed env = %v, want %v", settings.Env, want)
	}
	recipe, err := GenerateDockerfile("base", []string{"yolo.chat.skill"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recipe, "printf '%s\\n' '"+aircClaudeSettingsJSON()+"' > "+aircClaudeSettings+"\n") {
		t.Fatal("the drop-in must be written verbatim as one single-quoted word")
	}
	if strings.Contains(recipe, "/home/sandbox/.claude") || strings.Contains(recipe, "/etc/claude-code/managed-settings.json") {
		t.Fatal("delivery must not write the shared account leaf or replace the base managed settings")
	}
}

// The Repartee client and its airc-box launcher are gone. Their feature
// names fail with one explanation, are not aliases and no longer block a
// custom feature of that name.
func TestRemovedChatClientFeatures(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	for _, name := range []string{"yolo.chat.server", "yolo.chat.repartee"} {
		if _, err := Lookup(name); err == nil || !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "yolo.chat.skill") || !strings.Contains(err.Error(), "yoloctl chat") {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := GenerateDockerfile("base", []string{"yolo.chat.skill", name}); err == nil {
			t.Errorf("%s still composes", name)
		}
		if IsBuiltin(name) || strings.Contains(strings.Join(Names(), ","), name) {
			t.Errorf("%s is still listed", name)
		}
	}
	writeUserFeature(t, testFeaturesDir(t), "yolo.chat.server", "name: yolo.chat.server\ndescription: Custom\n")
	if f, err := Lookup("yolo.chat.server"); err != nil || f.Description != "Custom" {
		t.Fatalf("custom feature: %+v %v", f, err)
	}
}

func TestAIRCDockerfileLineLengths(t *testing.T) {
	for _, name := range []string{"yolo.chat.skill"} {
		t.Run(name, func(t *testing.T) {
			recipe, err := GenerateDockerfile("base", []string{name})
			if err != nil {
				t.Fatal(err)
			}
			// BuildKit scans physical Dockerfile lines with bufio's default
			// token limit. An inline base64 archive used to exceed that limit.
			scanner := bufio.NewScanner(strings.NewReader(recipe))
			for scanner.Scan() {
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("Dockerfile exceeds BuildKit's physical line limit: %v", err)
			}
		})
	}
}

func TestAIRCDockerfileScripts(t *testing.T) {
	recipe, err := GenerateDockerfile("base", []string{"yolo.chat.skill"})
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(recipe, "<<'AIRC_INSTALL'\n")
	if !ok {
		t.Fatal("missing build script")
	}
	script, _, ok := strings.Cut(rest, "\nAIRC_INSTALL\n")
	if !ok || !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Fatal("script must have a shebang so BuildKit keeps the payload out of argv")
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid installation script: %v\n%s", err, out)
	}
	fetch := strings.Index(script, "curl -fsSL")
	verify := strings.Index(script, "sha256sum -c -")
	extract := strings.Index(script, "tar -xzf")
	if fetch < 0 || verify <= fetch || extract <= verify {
		t.Fatal("the release archive must be downloaded and verified before extraction")
	}
}
