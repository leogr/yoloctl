package main

import (
	"reflect"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

func TestApplyLaunchEnv(t *testing.T) {
	claude := &agentprofile.Profile{
		Name:               "claude",
		Command:            "claude",
		StateContainerPath: "/home/sandbox/.claude",
		LaunchEnv:          map[string]string{"CLAUDE_CONFIG_DIR": "{{state_dir}}"},
	}
	multi := &agentprofile.Profile{
		Name:               "multi",
		StateContainerPath: "/home/sandbox/.claude",
		// Two keys, declared out of order, to prove sorted output.
		LaunchEnv: map[string]string{"ZED": "z", "ABC": "{{state_dir}}/sub"},
	}
	bare := &agentprofile.Profile{Name: "codex", Command: "codex"} // no launch_env

	cases := []struct {
		name    string
		profile *agentprofile.Profile
		argv    []string
		want    []string
	}{
		{
			"claude collapse",
			claude,
			[]string{"claude", "--dangerously-skip-permissions"},
			[]string{"env", "CLAUDE_CONFIG_DIR=/home/sandbox/.claude", "claude", "--dangerously-skip-permissions"},
		},
		{
			"sorted keys + token expansion mid-value",
			multi,
			[]string{"claude"},
			[]string{"env", "ABC=/home/sandbox/.claude/sub", "ZED=z", "claude"},
		},
		{"no launch_env is a passthrough", bare, []string{"codex", "--foo"}, []string{"codex", "--foo"}},
		{"nil profile is a passthrough", nil, []string{"bash"}, []string{"bash"}},
		{"empty launch_env is a passthrough", &agentprofile.Profile{LaunchEnv: map[string]string{}}, []string{"sh"}, []string{"sh"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applyLaunchEnv(c.profile, c.argv); !reflect.DeepEqual(got, c.want) {
				t.Errorf("applyLaunchEnv() = %v, want %v", got, c.want)
			}
		})
	}
}

// The prefix must not alias or mutate the caller's argv slice — both
// run.go and session_client.go reuse the base argv elsewhere.
func TestApplyLaunchEnv_DoesNotMutateInput(t *testing.T) {
	p := &agentprofile.Profile{
		StateContainerPath: "/home/sandbox/.claude",
		LaunchEnv:          map[string]string{"CLAUDE_CONFIG_DIR": "{{state_dir}}"},
	}
	argv := []string{"claude", "--flag"}
	_ = applyLaunchEnv(p, argv)
	if !reflect.DeepEqual(argv, []string{"claude", "--flag"}) {
		t.Errorf("applyLaunchEnv mutated its input argv: %v", argv)
	}
}

func TestResolveSessionArgv_AppendsAgentExtraArgs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg := &agentbox.Config{
		Agent:          "claude",
		AgentExtraArgs: []string{"--model", "opus"},
	}
	got, err := resolveSessionArgv("default", cfg)
	if err != nil {
		t.Fatalf("resolveSessionArgv: %v", err)
	}
	want := []string{
		"env",
		"CLAUDE_CONFIG_DIR=/home/sandbox/.claude",
		"DISABLE_AUTOUPDATER=1",
		"FORCE_AUTOUPDATE_PLUGINS=1",
		"claude",
		"--dangerously-skip-permissions",
		"--model",
		"opus",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolveSessionArgv() = %v, want %v", got, want)
	}
}
