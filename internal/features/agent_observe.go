package features

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed agent-observe.py
var agentObserveScript string

// Claude Code runs the observation writer from an image-level managed-settings
// drop-in, never through the shared account leaf: as its status line (model,
// effort, context and version, after each assistant message and at start or
// resume) and as async hooks (conversation identity, plus model on a startup
// and effort on each Stop, plus working, idle and attention states). The writer
// is inert unless the controller passed a launch generation. Its interpreter
// is guaranteed at compose time: the build
// resolves Python 3.8+ from the base, installs the distribution's python3
// through apt when the base has none, and otherwise fails, so no image claims
// observation without a working writer.
const (
	agentObserveHook       = "/usr/local/libexec/yoloctl/agent-observe"
	agentObserveStatusLine = "/usr/local/libexec/yoloctl/agent-statusline"
	agentObserveSettings   = "/etc/claude-code/managed-settings.d/40-yoloctl-agent.json"
)

// Codex runs the same writer from System-layer hooks in /etc/codex/config.toml,
// which Codex loads as managed hooks with no per-hook trust prompt. The account's
// own ~/.codex/config.toml and hooks.json are never touched. The launcher is
// separate because the shared hook launcher passes no mode.
const (
	agentObserveCodexHook   = "/usr/local/libexec/yoloctl/agent-observe-codex"
	agentObserveCodexConfig = "/etc/codex/config.toml"
	// AgentObserveCodexQuery is the controller's refresh read of a Codex box:
	// `agent-observe-codex-query GEN THREAD`, run through the confined exec.
	AgentObserveCodexQuery = "/usr/local/libexec/yoloctl/agent-observe-codex-query"
)

// agy has no system settings layer: its status line is the account's own
// (~/.gemini/antigravity-cli/settings.json). This feature installs only the
// writer and the launcher that statusLine names; the agy launcher seeds it in
// a new account's settings, and `yoloctl account observe` adds it to an
// existing one, keeping the command it replaces to run after recording.
const AgentObserveAgyStatusLine = "/usr/local/libexec/yoloctl/agent-statusline-agy"

func agentObserveAgyInstallDockerfile() string {
	return agentObserveWriterInstall("yolo.agent.observe-agy", nil,
		[]agentObserveLauncher{{AgentObserveAgyStatusLine, "agy", false}}, "")
}

// agentObserveCodexEvents are the Codex hooks the contract keeps: identity,
// model, working and idle, permission waits and the tokens used per model
// request. Codex runs an async SessionEnd hook synchronously, so that one is
// bounded instead.
var agentObserveCodexEvents = []string{"SessionStart", "Stop", "SessionEnd", "SubagentStart", "UserPromptSubmit", "PostModelRequest", "PermissionRequest"}

// agentObserveCodexTOML is the System-layer config: only the [hooks] table
// (Codex config/src/hook_config.rs), no feature flags or model keys.
func agentObserveCodexTOML() string {
	var b strings.Builder
	b.WriteString("# yolo.agent.observe-codex: session and model reports for this launch.\n")
	for _, event := range agentObserveCodexEvents {
		fmt.Fprintf(&b, "\n[[hooks.%s]]\n[[hooks.%s.hooks]]\ntype = \"command\"\ncommand = %q\n", event, event, agentObserveCodexHook)
		if event == "SessionEnd" {
			b.WriteString("timeout = 2\n")
		} else {
			b.WriteString("async = true\n")
		}
	}
	return b.String()
}

func agentObserveCodexInstallDockerfile() string {
	return agentObserveWriterInstall("yolo.agent.observe-codex", []string{"/etc/codex"},
		[]agentObserveLauncher{{agentObserveCodexHook, "codex", false}, {AgentObserveCodexQuery, "codex-query", true}},
		fmt.Sprintf("cat > %s <<'AGENT_OBSERVE_CODEX'\n%sAGENT_OBSERVE_CODEX", agentObserveCodexConfig, agentObserveCodexTOML()))
}

// agentObserveEvents are the hooks the contract keeps. Stop fires for the
// main agent only; subagent-tagged events never update box values.
var agentObserveEvents = []string{"SessionStart", "SessionEnd", "PostModelSwitch", "Stop", "UserPromptSubmit", "Notification", "StopFailure"}

// agentObserveSettingsJSON is the drop-in. The managed statusLine wins over
// the user's own, which the writer then runs itself (D2). Hooks are async, so
// Claude neither waits for nor times them out; the writer bounds itself (a
// 0.5 s lock wait, one append). The status line has no refreshInterval: it
// runs only on Claude's own triggers.
func agentObserveSettingsJSON() string {
	type command struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Async   bool   `json:"async,omitempty"`
	}
	type matcher struct {
		Hooks []command `json:"hooks"`
	}
	hooks := map[string][]matcher{}
	for _, event := range agentObserveEvents {
		hooks[event] = []matcher{{Hooks: []command{{Type: "command", Command: agentObserveHook, Async: true}}}}
	}
	settings, err := json.Marshal(map[string]any{
		"statusLine": command{Type: "command", Command: agentObserveStatusLine},
		"hooks":      hooks,
	})
	if err != nil {
		panic(err)
	}
	return string(settings)
}

// agentObservePython is the writer's interpreter check: 3.8+ with the
// modules it imports.
const agentObservePython = `-I -S -c 'import fcntl, json, os, re, sys, time; sys.exit(sys.version_info < (3, 8))'`

// agentObserveLauncher is one mode's entry point into the shared writer;
// passArgs forwards the launcher's own arguments to that mode.
type agentObserveLauncher struct {
	path, mode string
	passArgs   bool
}

// agentObserveWriterInstall installs the shared writer for one observe
// feature: the interpreter check (installing python3 through apt when the
// base has none), agent-observe.py, one launcher per mode and the feature's
// own configuration in tail. Every observe feature installs the same writer,
// so selecting several of them stays consistent.
func agentObserveWriterInstall(feature string, dirs []string, launchers []agentObserveLauncher, tail string) string {
	var b strings.Builder
	paths := make([]string, 0, len(launchers))
	for _, l := range launchers {
		target := "/usr/local/lib/yoloctl/agent-observe.py"
		if l.mode != "" {
			target += " " + l.mode
		}
		if l.passArgs {
			target += ` \"\$@\"`
		}
		fmt.Fprintf(&b, "printf '%%s\\n' '#!/bin/sh' \"exec $python -I -S %s\" > %s\n", target, l.path)
		paths = append(paths, l.path)
	}
	return fmt.Sprintf(`RUN <<'AGENT_OBSERVE_INSTALL'
#!/bin/sh
set -eu
python=
for candidate in /usr/local/bin/python3 /usr/bin/python3; do
    if [ -x "$candidate" ] && "$candidate" %s; then python=$candidate; break; fi
done
if [ -z "$python" ] && command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y --no-install-recommends python3
    rm -rf /var/lib/apt/lists/*
    if /usr/bin/python3 %s; then python=/usr/bin/python3; fi
fi
if [ -z "$python" ]; then
    echo '%s needs Python 3.8+ (python3) in the image; use a base that provides it or apt-get' >&2
    exit 1
fi
install -d %s
base64 -d > /usr/local/lib/yoloctl/agent-observe.py <<'AGENT_OBSERVE_SOURCE'
%sAGENT_OBSERVE_SOURCE
%schmod 0755 %s
%s
AGENT_OBSERVE_INSTALL`, agentObservePython, agentObservePython, feature,
		strings.Join(append([]string{"/usr/local/lib/yoloctl", "/usr/local/libexec/yoloctl"}, dirs...), " "),
		aircBase64Lines([]byte(agentObserveScript)), b.String(), strings.Join(paths, " "), tail)
}

func agentObserveInstallDockerfile() string {
	return agentObserveWriterInstall("yolo.agent.observe", []string{"/etc/claude-code/managed-settings.d"},
		[]agentObserveLauncher{{agentObserveHook, "", false}, {agentObserveStatusLine, "statusline", false}},
		fmt.Sprintf("printf '%%s\\n' '%s' > %s", agentObserveSettingsJSON(), agentObserveSettings))
}
