package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

// TestAgentProfiles_BuiltIns verifies the embedded catalog loads through
// Lookup as the expected structs without writing into the config dir.
func TestAgentProfiles_BuiltIns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	tests := []struct {
		agent string
		want  agentprofile.Profile
	}{
		{"claude", agentprofile.Profile{
			Name:               "claude",
			Image:              "yoloctl/agentbox:ubuntu-24.04",
			Command:            "claude",
			DefaultArgs:        []string{"--dangerously-skip-permissions"},
			NpmPackage:         "@anthropic-ai/claude-code",
			StateContainerPath: "/home/sandbox/.claude",
			StateMounts:        []agentprofile.StateMount{{Source: "anthropic", Destination: "/home/sandbox/.config/anthropic"}},
			LaunchEnv: map[string]string{
				"CLAUDE_CONFIG_DIR":        "{{state_dir}}",
				"DISABLE_AUTOUPDATER":      "1",
				"FORCE_AUTOUPDATE_PLUGINS": "1",
			},
			Auth: &agentprofile.AuthSpec{
				CredentialFile: ".credentials.json",
				Format:         agentprofile.AuthFormatClaude, ProfilesDir: "anthropic",
				ImportSecret: &agentprofile.ImportSecret{
					File: ".claude.json", Field: "primaryApiKey", Secret: "ANTHROPIC_API_KEY",
				},
			},
			RequiresFeatures: []string{"claude", "github", "yolo.agent.observe"},
			Session: &agentprofile.SessionSpec{
				IDFormat:     agentprofile.SessionIDFormatUUID,
				AssignArgs:   []string{"--session-id", "{{session_id}}"},
				ResumeArgv:   []string{"{{command}}", "--resume", "{{session_id}}"},
				ReservedArgs: []string{"--session-id", "--resume", "-r", "--continue", "-c", "--fork-session", "--bg"},
				Observe:      &agentprofile.ObserveSpec{Push: "yolo.agent.observe", Selection: agentprofile.SelectionReported},
			},
			SecretInjection: []agentprofile.SecretInjection{{Secret: "ANTHROPIC_API_KEY"}},
		}},
		{"codex", agentprofile.Profile{
			Name:               "codex",
			Image:              "yoloctl/agentbox:ubuntu-24.04",
			Command:            "codex",
			DefaultArgs:        []string{"--ask-for-approval", "never", "--sandbox", "danger-full-access"},
			NpmPackage:         "@openai/codex",
			StateContainerPath: "/home/sandbox/.codex",
			StateArchiveExcludes: []string{
				"tmp/arg0", "tmp/arg0-linux", "app-server-control/app-server-control.sock",
				"app-server-daemon/daemon.pid", "app-server-daemon/daemon-updater.pid",
				"app-server-daemon/app-server.pid", "app-server-daemon/app-server-updater.pid",
			},
			Auth: &agentprofile.AuthSpec{
				CredentialFile: "auth.json",
				Format:         agentprofile.AuthFormatCodex,
			},
			RequiresFeatures: []string{"codex", "github", "yolo.agent.observe-codex"},
			Session: &agentprofile.SessionSpec{
				IDFormat:     "uuid",
				ResumeArgv:   []string{"{{command}}", "resume", "{{session_id}}"},
				ReservedArgs: []string{"resume"},
				Observe:      &agentprofile.ObserveSpec{Push: "yolo.agent.observe-codex", Selection: agentprofile.SelectionUnknown},
				Daemons:      []string{"codex app-server"},
			},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			got, err := agentprofile.Lookup(tt.agent)
			if err != nil {
				t.Fatalf("agentprofile.Lookup(%q): %v", tt.agent, err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("agentprofile.Lookup(%q) = %+v, want %+v", tt.agent, *got, tt.want)
			}
		})
	}
}

// TestAgentProfileNamesStable verifies the names list is exactly the embedded
// built-ins, sorted. User-added profiles would also show up here — covered in
// internal/agentprofile/profile_test.go.
func TestAgentProfileNamesStable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	want := []string{"agy", "claude", "codex", "gemini", "opencode", "pi"}
	got := agentProfileNames()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agentProfileNames() = %v, want %v", got, want)
	}
	for _, name := range got {
		if _, err := agentprofile.Lookup(name); err != nil {
			t.Errorf("agentProfileNames advertises %q but Lookup failed: %v", name, err)
		}
	}
}

// A PTY is requested only for real terminal input AND output; pipes and
// /dev/null must preserve stream bytes. PTY cases require fs.pty.allocate.
func TestExecWantsPTY(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = pr.Close() }()
	defer func() { _ = pw.Close() }()

	if execWantsPTY(devnull, pw) {
		t.Error("no terminal on either side must not ask for a PTY")
	}

	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no PTY available (%v); the terminal cases run where PTYs exist", err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	tests := []struct {
		name          string
		stdin, stdout *os.File
		want          bool
	}{
		{"terminal on both sides", slave, slave, true},
		{"stdout captured or piped", slave, pw, false},
		{"stdout redirected to a character device", slave, devnull, false},
		{"stdin piped", pr, slave, false},
		{"stdin at /dev/null", devnull, slave, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := execWantsPTY(tt.stdin, tt.stdout); got != tt.want {
				t.Errorf("execWantsPTY = %v, want %v", got, tt.want)
			}
		})
	}
}
