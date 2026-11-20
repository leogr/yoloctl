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
	if !IsAgentStateParentsRuntime(name) {
		return Feature{}, false, nil
	}
	dockerfile, err := agentStateParentsDockerfile(name, "")
	if err != nil {
		return Feature{}, true, err
	}
	return Feature{
		Name:    name,
		Install: &Install{Kind: installAgentStateParents, Dockerfile: dockerfile},
	}, true, nil
}

// OwnedAgentStateParentsDockerfile creates and owns just the new parents when
// the base image would otherwise be used directly.
func OwnedAgentStateParentsDockerfile(name string, uid, gid int) (string, error) {
	return agentStateParentsDockerfile(name, fmt.Sprintf("%d:%d", uid, gid))
}

func agentStateParentsDockerfile(name, owner string) (string, error) {
	escaped, ok := strings.CutPrefix(name, agentStateParentsPrefix)
	parent, err := url.PathUnescape(escaped)
	if !ok || err != nil || !strings.HasPrefix(parent, agentHome+"/") || path.Clean(parent) != parent || url.PathEscape(parent) != escaped {
		return "", fmt.Errorf("invalid agent state parents runtime feature %q", name)
	}
	var dirs []string
	for dir := parent; dir != agentHome; dir = path.Dir(dir) {
		dirs = append(dirs, dir)
	}
	slices.Reverse(dirs)

	// Compose-time shell arguments carry paths as data. Leave intentional
	// symlink layouts alone. Recipes with installers use the common home
	// repair. A recipe that only prepares parents owns just its new folders.
	setup := `home=$1
shift
if [ -L "${home%/*}" ] || [ -L "$home" ]; then exit 0; fi
for dir do
    if [ -L "$dir" ]; then exit 0; fi`
	if owner != "" {
		setup += `
    if [ -d "$dir" ]; then continue; fi`
	}
	setup += `
    mkdir -p -- "$dir"`
	if owner != "" {
		setup += fmt.Sprintf(`
    chown -h -- %s "$dir"`, owner)
	}
	setup += "\ndone"
	args, err := json.Marshal(append([]string{"/bin/sh", "-ceu", setup, "agent-state-parents", agentHome}, dirs...))
	if err != nil {
		return "", err
	}
	return "USER root\nRUN " + string(args), nil
}
