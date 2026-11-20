package features

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
)

const (
	agentStateParentsPrefix  = "yoloctl:agent-state-parents:"
	installAgentStateParents = "agent-state-parents"
	agentHome                = "/home/sandbox"
)

// AgentStateParentsRuntime selects image setup for the intermediate
// directories of a state bind inside the sandbox home. The native leaf is
// added later by the runtime; paths outside the home need no setup here.
func AgentStateParentsRuntime(statePath string) string {
	parent := path.Dir(path.Clean(statePath))
	if !strings.HasPrefix(parent, agentHome+"/") {
		return ""
	}
	return agentStateParentsPrefix + url.PathEscape(parent)
}

// IsAgentStateParentsRuntime recognizes the internal setup in a rendered
// installation plan.
func IsAgentStateParentsRuntime(name string) bool {
	return strings.HasPrefix(name, agentStateParentsPrefix)
}

func agentStateParentsFeature(name string) (Feature, bool, error) {
	escaped, ok := strings.CutPrefix(name, agentStateParentsPrefix)
	if !ok {
		return Feature{}, false, nil
	}
	parent, err := url.PathUnescape(escaped)
	if err != nil || !strings.HasPrefix(parent, agentHome+"/") || path.Clean(parent) != parent || url.PathEscape(parent) != escaped {
		return Feature{}, true, fmt.Errorf("invalid agent state parents runtime feature %q", name)
	}
	var dirs []string
	for dir := parent; dir != agentHome; dir = path.Dir(dir) {
		dirs = append(dirs, dir)
	}
	slices.Reverse(dirs)

	// Compose-time shell arguments carry paths as data. Leave intentional
	// symlink layouts alone. The final image home repair sets ownership on
	// the real directories before the runtime applies the leaf bind.
	const setup = `home=$1
shift
if [ -L "${home%/*}" ] || [ -L "$home" ]; then exit 0; fi
for dir do
    if [ -L "$dir" ]; then exit 0; fi
    mkdir -p -- "$dir"
done`
	args, err := json.Marshal(append([]string{"/bin/sh", "-ceu", setup, "agent-state-parents", agentHome}, dirs...))
	if err != nil {
		return Feature{}, true, err
	}
	return Feature{
		Name:    name,
		Install: &Install{Kind: installAgentStateParents, Dockerfile: "USER root\nRUN " + string(args)},
	}, true, nil
}
