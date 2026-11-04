package features

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// AIRC remains a separate public project. Image builds fetch its published
// static release archive for the build architecture and verify the pinned
// SHA-256 before extraction; nothing is compiled and no source is embedded.
//
//go:embed airc-source/manifest.json
var aircManifest []byte

// Claude Code loads the upstream delivery plugin through an image-level
// managed-settings drop-in, never through the shared account leaf. Its monitor
// stays idle until the managed `space` client is connected.
const (
	aircClaudePlugin   = "/usr/local/share/airc/claude-plugin"
	aircClaudeSettings = "/etc/claude-code/managed-settings.d/50-airc-delivery.json"
)

func aircClaudeSettingsJSON() string {
	settings, err := json.Marshal(map[string]map[string]string{"env": {
		"CLAUDE_CODE_PLUGIN_DIRS": aircClaudePlugin,
		"AIRC_DELIVERY_SESSION":   "space",
	}})
	if err != nil {
		panic(err)
	}
	return string(settings)
}

type aircAsset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type aircRelease struct {
	Repository string               `json:"repository"`
	Release    string               `json:"release"`
	Revision   string               `json:"revision"`
	Assets     map[string]aircAsset `json:"assets"`
}

func aircReleaseManifest() aircRelease {
	var manifest aircRelease
	decoder := json.NewDecoder(bytes.NewReader(aircManifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		panic(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if a := manifest.Assets[arch]; a.URL == "" || a.SHA256 == "" {
			panic("AIRC release manifest lacks the " + arch + " asset")
		}
	}
	return manifest
}

func aircInstallDockerfile() string {
	manifest := aircReleaseManifest()
	amd64, arm64 := manifest.Assets["amd64"], manifest.Assets["arm64"]
	// The archive holds static bin/{airc,aircd,airc-codex}, LICENSE,
	// skills/airc and integrations/claude-plugin. The shebang makes BuildKit
	// execute a script file.
	install := fmt.Sprintf(`RUN <<'AIRC_INSTALL'
#!/bin/sh
set -eu
if [ "$(cat /usr/local/share/airc/revision 2>/dev/null)" != '%s' ]; then
    case "$(uname -m)" in
        x86_64) AIRC_URL='%s' AIRC_SHA256='%s' ;;
        aarch64) AIRC_URL='%s' AIRC_SHA256='%s' ;;
        *) echo "AIRC release has no archive for $(uname -m)" >&2; exit 1 ;;
    esac
    mkdir -p /tmp/yoloctl-airc/release /usr/local/share/airc
    curl -fsSL --proto '=https' --tlsv1.2 "$AIRC_URL" -o /tmp/yoloctl-airc/release.tar.gz
    printf '%%s  %%s\n' "$AIRC_SHA256" /tmp/yoloctl-airc/release.tar.gz | sha256sum -c -
    tar -xzf /tmp/yoloctl-airc/release.tar.gz --no-same-owner -C /tmp/yoloctl-airc/release
    install -m 0755 /tmp/yoloctl-airc/release/bin/airc /tmp/yoloctl-airc/release/bin/aircd /tmp/yoloctl-airc/release/bin/airc-codex /usr/local/bin/
    rm -rf /usr/local/share/airc/skill
    cp -R /tmp/yoloctl-airc/release/skills/airc /usr/local/share/airc/skill
    rm -rf %s
    cp -R /tmp/yoloctl-airc/release/integrations/claude-plugin %s
    test -x %s/scripts/airc-delivery
    install -m 0644 /tmp/yoloctl-airc/release/LICENSE /usr/local/share/airc/LICENSE
    install -d /etc/codex/skills /etc/claude-code/.claude/skills /etc/claude-code/managed-settings.d
    printf '%%s\n' '%s' > %s
    install -d -o sandbox -g sandbox /home/sandbox/.agents /home/sandbox/.agents/skills
    ln -sfn /usr/local/share/airc/skill /etc/codex/skills/airc
    ln -sfn /usr/local/share/airc/skill /etc/claude-code/.claude/skills/airc
    ln -sfn /usr/local/share/airc/skill /home/sandbox/.agents/skills/airc
    printf '%%s\n' '%s' > /usr/local/share/airc/revision
    rm -rf /tmp/yoloctl-airc
fi
AIRC_INSTALL`, manifest.Revision, amd64.URL, amd64.SHA256, arm64.URL, arm64.SHA256,
		aircClaudePlugin, aircClaudePlugin, aircClaudePlugin, aircClaudeSettingsJSON(), aircClaudeSettings,
		manifest.Revision)
	return install
}

// aircBase64Lines wraps embedded file bytes for a Dockerfile heredoc.
func aircBase64Lines(data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(encoded) > 0 {
		n := min(76, len(encoded))
		b.WriteString(encoded[:n])
		b.WriteByte('\n')
		encoded = encoded[n:]
	}
	return b.String()
}
