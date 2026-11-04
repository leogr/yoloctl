package main

import (
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/agentprofile"
)

// applyLaunchEnv prepends the profile's launch_env as an `env K=V … <argv>`
// command prefix, expanding the {{state_dir}} token to the profile's
// StateContainerPath. Returns argv unchanged when the profile is nil or
// declares no launch_env (codex, user profiles without it).
//
// Why a command prefix and not `nerdctl -e`: the openshell-sandbox russh
// session resets the agent's environment to a fixed allowlist, so -e
// injection is invisible to the agent; an env prefix on the launched command
// survives. The motivating use is
// CLAUDE_CONFIG_DIR, which relocates claude's stray ~/.claude.json into the
// persisted state mount so it survives `box recreate`.
//
// launch_env carries NON-SECRET config only (paths): the command line is
// visible in /proc/*/cmdline, so secrets must never be delivered this way
// (see userEnvironmentJSON for secret delivery). Keys are emitted in sorted
// order so the rendered command line is deterministic.
func applyLaunchEnv(profile *agentprofile.Profile, argv []string) []string {
	if profile == nil || len(profile.LaunchEnv) == 0 {
		return argv
	}
	keys := make([]string, 0, len(profile.LaunchEnv))
	for k := range profile.LaunchEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	prefixed := make([]string, 0, len(keys)+1+len(argv))
	prefixed = append(prefixed, "env")
	for _, k := range keys {
		v := strings.ReplaceAll(profile.LaunchEnv[k], "{{state_dir}}", profile.StateContainerPath)
		prefixed = append(prefixed, k+"="+v)
	}
	return append(prefixed, argv...)
}
