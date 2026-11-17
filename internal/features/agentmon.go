package features

import (
	"encoding/json"
	"fmt"
)

// AgentMon Live is a Claude Code plugin whose web view runs on the host. A box
// cannot reach it, so the plugin writes each session's snapshot to
// /var/lib/agentmon-live/spool when that directory exists, and the host's
// AgentMon server reads it. The feature installs the plugin; a declared box
// mount supplies the directory. Without the mount the plugin behaves as on a
// host.
//
// The source archive is the commit of release v0.39.0, checked by SHA-256
// before extraction. Nothing is compiled.
const (
	agentmonRevision = "5794505e5c230ef71d617bb19aef518f4b339ef1"
	agentmonSHA256   = "2c73cb43c19065f8696c01b6db59527bf2fa07ca82f10b5a36d0902e40d6e996"
	agentmonPlugin   = "/usr/local/share/agentmon-live"
	agentmonSettings = "/etc/claude-code/managed-settings.d/60-agentmon.json"
)

func agentmonArchiveURL() string {
	return "https://github.com/codenotary/agentmon-live/archive/" + agentmonRevision + ".tar.gz"
}

// agentmonSettingsJSON is the drop-in. It sorts after AIRC's, whose plugin
// directory it must therefore repeat: CLAUDE_CODE_PLUGIN_DIRS is one
// colon-separated value, and a directory that is absent is skipped.
func agentmonSettingsJSON() string {
	settings, err := json.Marshal(map[string]map[string]string{"env": {
		"CLAUDE_CODE_PLUGIN_DIRS": aircClaudePlugin + ":" + agentmonPlugin,
	}})
	if err != nil {
		panic(err)
	}
	return string(settings)
}

func agentmonInstallDockerfile() string {
	return fmt.Sprintf(`RUN <<'AGENTMON_INSTALL'
#!/bin/sh
set -eu
if [ "$(cat %[1]s/.yoloctl-revision 2>/dev/null)" != '%[2]s' ]; then
    mkdir -p /tmp/yoloctl-agentmon
    curl -fsSL --proto '=https' --tlsv1.2 '%[3]s' -o /tmp/yoloctl-agentmon/source.tar.gz
    printf '%%s  %%s\n' '%[4]s' /tmp/yoloctl-agentmon/source.tar.gz | sha256sum -c -
    rm -rf %[1]s
    mkdir -p %[1]s
    tar -xzf /tmp/yoloctl-agentmon/source.tar.gz --no-same-owner --strip-components=1 -C %[1]s
    test -f %[1]s/.claude-plugin/plugin.json
    install -d /etc/claude-code/managed-settings.d
    printf '%%s\n' '%[5]s' > %[6]s
    printf '%%s\n' '%[2]s' > %[1]s/.yoloctl-revision
    rm -rf /tmp/yoloctl-agentmon
fi
AGENTMON_INSTALL`, agentmonPlugin, agentmonRevision, agentmonArchiveURL(), agentmonSHA256, agentmonSettingsJSON(), agentmonSettings)
}
