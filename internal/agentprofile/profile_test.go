package agentprofile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
)

// setupTestConfig points XDG_CONFIG_HOME at a fresh tmpdir.
func setupTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

// TestIsBuiltin: the shipped agents report true; a user name false.
func TestIsBuiltin(t *testing.T) {
	if !IsBuiltin("agy") || !IsBuiltin("claude") || !IsBuiltin("codex") || !IsBuiltin("gemini") || !IsBuiltin("opencode") || !IsBuiltin("pi") {
		t.Error("agy/claude/codex/gemini/opencode/pi should be built-in agents")
	}
	if IsBuiltin("my-claude") {
		t.Error("my-claude should not be a built-in")
	}
}

func TestNativeReleaseProfileValidation(t *testing.T) {
	p := &Profile{Name: "my-native", Image: "base", Command: "agy", NativeRelease: "antigravity"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.NpmPackage = "other-package"
	if err := p.Validate(); err == nil {
		t.Fatal("ambiguous distribution accepted")
	}
	p.NpmPackage, p.NativeRelease = "", "arbitrary-shell"
	if err := p.Validate(); err == nil {
		t.Fatal("unknown native distribution accepted")
	}
}

func TestStateArchiveExclusionsStayBelowLeaf(t *testing.T) {
	setupTestConfig(t)
	p := &Profile{Name: "test-agent", Image: "base", Command: "agent", StateContainerPath: "/state", StateArchiveExcludes: []string{"tmp/runtime", "daemon.pid"}}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Lookup(p.Name)
	if err != nil || !slices.Equal(got.StateArchiveExcludes, p.StateArchiveExcludes) {
		t.Fatalf("archive policy not retained: %v, %v", got, err)
	}
	for _, name := range []string{"", ".", "..", "../other", "/state", "tmp/../auth.json", "tmp//runtime", "tmp/", "tmp\\runtime", "tmp\x00runtime", "tmp/*", "tmp/?", "tmp/[a]"} {
		p.StateArchiveExcludes = []string{name}
		if err := p.Validate(); err == nil {
			t.Errorf("unsafe/ambiguous exclusion %q accepted", name)
		}
	}
	p.StateArchiveExcludes = []string{"runtime"}
	p.StateContainerPath = ""
	if err := p.Validate(); err == nil {
		t.Fatal("archive exclusions without managed state accepted")
	}
}

// TestSaveAndDeleteCustomAgent: Save writes a custom profile the loader reads;
// Delete removes it. Save/Delete refuse a built-in name (edit/destroy are
// custom-only). A built-in's on-disk presence does NOT make it
// deletable.
func TestSaveAndDeleteCustomAgent(t *testing.T) {
	setupTestConfig(t)
	p := &Profile{Name: "my-claude", Image: "my-org/claude:custom", Command: "claude"}
	if err := p.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Lookup("my-claude")
	if err != nil {
		t.Fatalf("Lookup custom: %v", err)
	}
	if got.Image != "my-org/claude:custom" {
		t.Errorf("Image = %q, want my-org/claude:custom", got.Image)
	}
	if IsBuiltin("my-claude") {
		t.Error("custom profile must not report as built-in")
	}

	// Save under a built-in name is refused.
	bad := &Profile{Name: "claude", Image: "x", Command: "claude"}
	if err := bad.Save(); err == nil {
		t.Error("Save should refuse a built-in name")
	}
	// Delete refuses a built-in.
	if err := Delete("claude"); err == nil {
		t.Error("Delete should refuse a built-in name")
	}

	// Delete the custom one; Lookup then fails.
	if err := Delete("my-claude"); err != nil {
		t.Fatalf("Delete custom: %v", err)
	}
	if _, err := Lookup("my-claude"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Lookup after Delete: want fs.ErrNotExist, got %v", err)
	}
}

// TestBuiltinShape pins the embedded Claude definition to the model in force:
// the agent's egress comes from its REQUIRED FEATURES, so
// requires_features includes both the CLI-installing feature (claude) and
// github. There is no agent-level capability and no agent_feature field.
func TestBuiltinShape(t *testing.T) {
	setupTestConfig(t)
	p, err := Lookup("claude")
	if err != nil {
		t.Fatalf("lookup claude: %v", err)
	}
	if !containsAll(p.RequiresFeatures, "claude", "github", "yolo.agent.observe") {
		t.Errorf("claude built-in requires_features = %v, want claude, github and its reporter", p.RequiresFeatures)
	}
	want := &SessionSpec{
		IDFormat:     SessionIDFormatUUID,
		AssignArgs:   []string{"--session-id", "{{session_id}}"},
		ResumeArgv:   []string{"{{command}}", "--resume", "{{session_id}}"},
		ReservedArgs: []string{"--session-id", "--resume", "-r", "--continue", "-c", "--fork-session", "--bg"},
		Observe:      &ObserveSpec{Push: "yolo.agent.observe", Selection: SelectionReported},
	}
	if p.Session.SelectionReport() != SelectionReported || (&SessionSpec{}).SelectionReport() != SelectionUnknown {
		t.Error("Claude declares its in-app switches reported; the default is unknown")
	}
	if p.PushFeature() != "yolo.agent.observe" || !slices.Contains(p.RequiresFeatures, p.PushFeature()) {
		t.Error("the reporter is required (D1), so every Claude box gets it")
	}
	if !reflect.DeepEqual(p.Session, want) {
		t.Errorf("claude built-in session = %+v, want %+v", p.Session, want)
	}
	codex, err := Lookup("codex")
	if err != nil {
		t.Fatal(err)
	}
	wantCodex := &SessionSpec{
		IDFormat:     "uuid",
		ResumeArgv:   []string{"{{command}}", "resume", "{{session_id}}"},
		ReservedArgs: []string{"resume"},
		Observe:      &ObserveSpec{Push: "yolo.agent.observe-codex", Selection: SelectionUnknown},
		Daemons:      []string{"codex app-server"},
	}
	if !reflect.DeepEqual(codex.Session, wantCodex) || !slices.Contains(codex.RequiresFeatures, codex.PushFeature()) {
		t.Errorf("codex built-in session = %+v (features %v), want %+v", codex.Session, codex.RequiresFeatures, wantCodex)
	}
	// agy is observe-only: its status line reports, and yoloctl never
	// resumes its conversations.
	agy, err := Lookup("agy")
	if err != nil {
		t.Fatal(err)
	}
	wantAgy := &SessionSpec{IDFormat: "uuid", Observe: &ObserveSpec{Push: "yolo.agent.observe-agy", Selection: SelectionUnknown,
		AccountStatusLine: &AccountStatusLine{Settings: "antigravity-cli/settings.json", Command: "/usr/local/libexec/yoloctl/agent-statusline-agy"}}}
	if !reflect.DeepEqual(agy.Session, wantAgy) || agy.Session.Resumes() || !slices.Contains(agy.RequiresFeatures, agy.PushFeature()) {
		t.Errorf("agy built-in session = %+v (features %v), want %+v", agy.Session, agy.RequiresFeatures, wantAgy)
	}
	// Claude's and Codex's identities are verified; every other built-in
	// stays unsupported until its resume behaviour and IDs are proven.
	for _, name := range []string{"gemini", "opencode", "pi"} {
		other, err := Lookup(name)
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		if other.Session != nil {
			t.Errorf("%s built-in declares a session block; it must stay unsupported", name)
		}
	}
}

// containsAll reports whether haystack contains every needle.
func containsAll(haystack []string, needles ...string) bool {
	for _, n := range needles {
		if !slices.Contains(haystack, n) {
			return false
		}
	}
	return true
}

// TestLookup_BuiltIn loads claude through Lookup and asserts the
// values came from the embedded YAML.
func TestLookup_BuiltIn(t *testing.T) {
	setupTestConfig(t)
	p, err := Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup(claude): %v", err)
	}
	if p.Name != "claude" {
		t.Errorf("Name = %q, want claude", p.Name)
	}
	if p.Image == "" {
		t.Error("Image is empty")
	}
	if p.StateContainerPath != "/home/sandbox/.claude" {
		t.Errorf("StateContainerPath = %q, want /home/sandbox/.claude", p.StateContainerPath)
	}
	if p.Image != "yoloctl/agentbox:ubuntu-24.04" {
		t.Errorf("Image = %q, want current plain base image", p.Image)
	}
}

// TestLookup_BuiltInIgnoresLegacyInstalledCopy is the regression for upgrades
// from the retired per-agent image model. Older yoloctl versions wrote
// built-ins under agents/ and never refreshed them, so a current binary could
// otherwise keep composing from an obsolete image reference.
func TestLookup_BuiltInIgnoresLegacyInstalledCopy(t *testing.T) {
	setupTestConfig(t)
	dir, err := layout.AgentsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := []byte("name: claude\nimage: legacy.invalid/claude:old\ncommand: claude\n")
	if err := os.WriteFile(filepath.Join(dir, "claude.yaml"), stale, 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup(claude): %v", err)
	}
	if p.Image != "yoloctl/agentbox:ubuntu-24.04" {
		t.Fatalf("Lookup used stale disk image %q instead of the embedded plain base", p.Image)
	}
	if !containsAll(p.RequiresFeatures, "claude", "github") {
		t.Fatalf("Lookup used stale disk profile without shipped features: %v", p.RequiresFeatures)
	}
}

// TestLookup_UnknownReturnsErrNotExist verifies that asking for a
// missing profile surfaces fs.ErrNotExist (callers errors.Is it to
// render a friendly "unknown agent" message).
func TestLookup_UnknownReturnsErrNotExist(t *testing.T) {
	setupTestConfig(t)
	_, err := Lookup("nope")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %v is not fs.ErrNotExist", err)
	}
}

// TestLookup_UserDefined verifies a user-dropped YAML file
// participates equally with the built-ins.
func TestLookup_UserDefined(t *testing.T) {
	setupTestConfig(t)
	dir, _ := layout.AgentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	custom := []byte(`name: my-claude
image: my-org/claude:custom
command: claude
default_args:
  - --my-flag
npm_package: "@anthropic-ai/claude-code"
state_container_path: /home/sandbox/.claude
`)
	if err := os.WriteFile(filepath.Join(dir, "my-claude.yaml"), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Lookup("my-claude")
	if err != nil {
		t.Fatalf("Lookup(my-claude): %v", err)
	}
	if p.Image != "my-org/claude:custom" {
		t.Errorf("Image = %q, want my-org/claude:custom", p.Image)
	}
	if len(p.DefaultArgs) != 1 || p.DefaultArgs[0] != "--my-flag" {
		t.Errorf("DefaultArgs = %v, want [--my-flag]", p.DefaultArgs)
	}
}

// TestLookup_NameMismatchRejected verifies the filename-must-equal-
// name invariant. A claude.yaml whose name: field says "my-claude"
// is a configuration mistake the loader must surface.
func TestLookup_NameMismatchRejected(t *testing.T) {
	setupTestConfig(t)
	dir, _ := layout.AgentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := []byte("name: my-claude\nimage: a\ncommand: c\n")
	path := filepath.Join(dir, "renamed.yaml")
	if err := os.WriteFile(path, bad, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Lookup("renamed")
	if err == nil {
		t.Fatal("want error on name/filename mismatch")
	}
	if !strings.Contains(err.Error(), "must equal filename") {
		t.Errorf("error %q missing expected substring", err.Error())
	}
}

// TestNames_SortedAndIncludesUser verifies Names() returns a sorted
// list including any user-added profiles.
func TestNames_SortedAndIncludesUser(t *testing.T) {
	setupTestConfig(t)
	dir, _ := layout.AgentsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	user := []byte("name: aaa-first\nimage: a\ncommand: c\n")
	if err := os.WriteFile(filepath.Join(dir, "aaa-first.yaml"), user, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Names()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"aaa-first", "agy", "claude", "codex", "gemini", "opencode", "pi"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestValidate covers Profile.Validate's invariants.
func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		wantErr string
	}{
		{"ok", Profile{Name: "claude", Image: "img", Command: "claude"}, ""},
		{"bad-name", Profile{Name: "with space", Image: "i", Command: "c"}, "invalid"},
		{"no-image", Profile{Name: "claude", Command: "c"}, "image is required"},
		{"no-command", Profile{Name: "claude", Image: "i"}, "command is required"},
		{"relative-state-path", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "rel/path"}, "must be absolute"},
		{"absolute-state-path-ok", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "/abs"}, ""},
		{"auth-ok", Profile{Name: "codex", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: "auth.json"}}, ""},
		{"auth-no-credential-file", Profile{Name: "codex", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{}}, "credential_file is required"},
		{"auth-absolute-credential-file", Profile{Name: "codex", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: "/etc/passwd"}}, "must be relative"},
		{"auth-traversal-credential-file", Profile{Name: "codex", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: "../../etc/x"}}, "must not escape"},
		{"auth-requires-state-path", Profile{Name: "codex", Image: "i", Command: "c",
			Auth: &AuthSpec{CredentialFile: "auth.json"}}, "requires state_container_path"},
		{"auth-import-home-ok", Profile{Name: "custom", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: "auth.json", ImportHomeDir: ".local/share/opencode"}}, ""},
		{"auth-import-home-absolute", Profile{Name: "custom", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: "auth.json", ImportHomeDir: "/etc"}}, "must be relative"},
		{"auth-import-home-traversal", Profile{Name: "custom", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: "auth.json", ImportHomeDir: "../outside"}}, "must not escape"},
		{"auth-import-files-ok", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: ".credentials.json", ImportFiles: []string{"config.json"}}}, ""},
		{"auth-import-files-traversal", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: ".credentials.json", ImportFiles: []string{"../../etc/x"}}}, "must not escape"},
		{"auth-import-files-absolute", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "/abs",
			Auth: &AuthSpec{CredentialFile: ".credentials.json", ImportFiles: []string{"/etc/passwd"}}}, "must be relative"},
		{"launch-env-ok", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "/home/sandbox/.claude",
			LaunchEnv: map[string]string{"CLAUDE_CONFIG_DIR": "{{state_dir}}"}}, ""},
		{"launch-env-state-dir-needs-state-path", Profile{Name: "claude", Image: "i", Command: "c",
			LaunchEnv: map[string]string{"CLAUDE_CONFIG_DIR": "{{state_dir}}"}}, "state_container_path is empty"},
		{"launch-env-bad-key", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "/abs",
			LaunchEnv: map[string]string{"BAD-NAME": "x"}}, "not a valid environment variable name"},
		{"launch-env-leading-digit-key", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "/abs",
			LaunchEnv: map[string]string{"1ABC": "x"}}, "not a valid environment variable name"},
		{"launch-env-unknown-token", Profile{Name: "claude", Image: "i", Command: "c", StateContainerPath: "/abs",
			LaunchEnv: map[string]string{"FOO": "{{bar}}"}}, "unknown template token"},
		{"launch-env-non-template-value-ok", Profile{Name: "claude", Image: "i", Command: "c",
			LaunchEnv: map[string]string{"FOO": "plain-value"}}, ""},
		{"session-ok", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			AssignArgs: []string{"--session-id", "{{session_id}}"}, ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"},
			ReservedArgs: []string{"--resume"}}}, ""},
		{"session-resume-only-ok", Profile{Name: "codex", Image: "i", Command: "codex", Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"codex", "resume", "{{session_id}}"}}}, ""},
		{"session-unknown-format", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "thr",
			ResumeArgv: []string{"{{command}}", "{{session_id}}"}}}, "id_format"},
		{"session-no-resume", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid"}}, "resume_argv is required"},
		{"session-observe-only-ok", Profile{Name: "agy", Image: "i", Command: "agy", RequiresFeatures: []string{"yolo.agent.observe-agy"}, Session: &SessionSpec{IDFormat: "uuid",
			Observe: &ObserveSpec{Push: "yolo.agent.observe-agy"}}}, ""},
		{"session-account-status-line-ok", Profile{Name: "agy", Image: "i", Command: "agy", StateContainerPath: "/home/sandbox/.gemini", RequiresFeatures: []string{"o"}, Session: &SessionSpec{IDFormat: "uuid",
			Observe: &ObserveSpec{Push: "o", AccountStatusLine: &AccountStatusLine{Settings: "antigravity-cli/settings.json", Command: "/usr/local/libexec/yoloctl/x"}}}}, ""},
		{"session-account-status-line-escapes", Profile{Name: "agy", Image: "i", Command: "agy", StateContainerPath: "/home/sandbox/.gemini", RequiresFeatures: []string{"o"}, Session: &SessionSpec{IDFormat: "uuid",
			Observe: &ObserveSpec{Push: "o", AccountStatusLine: &AccountStatusLine{Settings: "../settings.json", Command: "/x"}}}}, "must not escape"},
		{"session-account-status-line-unclean", Profile{Name: "agy", Image: "i", Command: "agy", StateContainerPath: "/home/sandbox/.gemini", RequiresFeatures: []string{"o"}, Session: &SessionSpec{IDFormat: "uuid",
			Observe: &ObserveSpec{Push: "o", AccountStatusLine: &AccountStatusLine{Settings: "a//settings.json", Command: "/x"}}}}, "clean path"},
		{"session-account-status-line-no-state", Profile{Name: "agy", Image: "i", Command: "agy", RequiresFeatures: []string{"o"}, Session: &SessionSpec{IDFormat: "uuid",
			Observe: &ObserveSpec{Push: "o", AccountStatusLine: &AccountStatusLine{Settings: "settings.json", Command: "/x"}}}}, "requires state_container_path"},
		{"session-account-status-line-shell", Profile{Name: "agy", Image: "i", Command: "agy", StateContainerPath: "/home/sandbox/.gemini", RequiresFeatures: []string{"o"}, Session: &SessionSpec{IDFormat: "uuid",
			Observe: &ObserveSpec{Push: "o", AccountStatusLine: &AccountStatusLine{Settings: "settings.json", Command: "/x; rm -rf /"}}}}, "plain absolute path"},
		{"session-observe-only-assigns", Profile{Name: "agy", Image: "i", Command: "agy", RequiresFeatures: []string{"yolo.agent.observe-agy"}, Session: &SessionSpec{IDFormat: "uuid",
			AssignArgs: []string{"--id", "{{session_id}}"}, Observe: &ObserveSpec{Push: "yolo.agent.observe-agy"}}}, "resume_argv is required"},
		{"session-observe-push-ok", Profile{Name: "claude", Image: "i", Command: "c", RequiresFeatures: []string{"yolo.agent.observe"}, Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, Observe: &ObserveSpec{Push: "yolo.agent.observe"}}}, ""},
		{"session-observe-push-not-required", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, Observe: &ObserveSpec{Push: "yolo.agent.observe"}}}, "must also be listed in requires_features"},
		{"session-observe-push-not-a-name", Profile{Name: "claude", Image: "i", Command: "c", RequiresFeatures: []string{"ghcr.io/x/y:1"}, Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, Observe: &ObserveSpec{Push: "ghcr.io/x/y:1"}}}, "observe.push"},
		{"session-observe-selection-reported", Profile{Name: "claude", Image: "i", Command: "c", RequiresFeatures: []string{"yolo.agent.observe"}, Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, Observe: &ObserveSpec{Push: "yolo.agent.observe", Selection: "reported"}}}, ""},
		{"session-observe-selection-unknown", Profile{Name: "claude", Image: "i", Command: "c", RequiresFeatures: []string{"yolo.agent.observe"}, Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, Observe: &ObserveSpec{Push: "yolo.agent.observe", Selection: "unknown"}}}, ""},
		{"session-observe-selection-bad", Profile{Name: "claude", Image: "i", Command: "c", RequiresFeatures: []string{"yolo.agent.observe"}, Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, Observe: &ObserveSpec{Push: "yolo.agent.observe", Selection: "always"}}}, "observe.selection"},
		{"session-observe-push-empty", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, Observe: &ObserveSpec{}}}, "observe.push"},
		{"session-observe-feature-renamed", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, LegacyObserveFeature: "yolo.agent.observe"}}, "observe_feature was renamed"},
		{"session-resume-without-id", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--continue"}}}, "must contain {{session_id}}"},
		{"session-assign-without-id", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			AssignArgs: []string{"--new"}, ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}}}, "must contain {{session_id}}"},
		{"session-command-token-only-in-resume", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			AssignArgs: []string{"{{command}}", "{{session_id}}"}, ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}}}, "unknown template token"},
		{"session-unknown-token", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}", "{{cwd}}"}}}, "unknown template token"},
		{"session-empty-arg", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "", "{{session_id}}"}}}, "empty argument"},
		{"session-templated-reserved-arg", Profile{Name: "claude", Image: "i", Command: "c", Session: &SessionSpec{IDFormat: "uuid",
			ResumeArgv: []string{"{{command}}", "--resume", "{{session_id}}"}, ReservedArgs: []string{"{{session_id}}"}}}, "literal argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.profile.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q missing %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestParseProfile_RejectsUnknownFields pins strict decoding: an agent
// definition routes credentials (secret_injection, auth, launch_env), so a
// misspelled field must fail CLOSED — with the lenient parser it silently
// vanished and the agent ran with different injection/auth behavior than the
// file's author declared. Table covers the security-sensitive spellings plus
// nested-block strictness.
func TestParseProfile_RejectsUnknownFields(t *testing.T) {
	setupTestConfig(t)
	base := "name: my-agent\nimage: img:1\ncommand: run\n"
	cases := []struct {
		name string
		yaml string
		want string // substring the error must carry
	}{
		{"misspelled secret_injection", base +
			"secret_injections:\n  - secret: ANTHROPIC_API_KEY\n", "secret_injections"},
		{"misspelled launch_env", base +
			"launch_environment:\n  CLAUDE_CONFIG_DIR: /x\n", "launch_environment"},
		{"misspelled auth block", base +
			"auth_files:\n  credential_file: auth.json\n", "auth_files"},
		{"unknown field inside auth", base +
			"state_container_path: /home/sandbox/.x\nauth:\n  credential_file: auth.json\n  credential_files: [a.json]\n", "credential_files"},
		{"unknown field inside secret_injection rule", base +
			"secret_injection:\n  - secret: K\n    environment: E\n", "environment"},
		{"removed agent-level capabilities", base +
			"requires_capabilities: [network.anthropic]\n", "requires_capabilities"},
		{"trailing document", base + "---\nname: stowaway\n", "trailing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseProfile([]byte(c.yaml), "test.yaml", "my-agent")
			if err == nil {
				t.Fatal("misspelled/unknown field accepted — must fail closed")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should carry %q", err.Error(), c.want)
			}
		})
	}
	// The exact correct spellings still parse (guards against the strict
	// decoder rejecting valid schema).
	good := base + `state_container_path: /home/sandbox/.x
launch_env:
  CLAUDE_CONFIG_DIR: "{{state_dir}}"
auth:
  credential_file: auth.json
secret_injection:
  - secret: ANTHROPIC_API_KEY
    env: ANTHROPIC_API_KEY
`
	if _, err := parseProfile([]byte(good), "test.yaml", "my-agent"); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
}

// Only Codex declares a daemon; a declared daemon must be plain words, since
// the holder embeds them in its reaper.
func TestSessionDaemons(t *testing.T) {
	setupTestConfig(t)
	names, err := BuiltinNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		p, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		want := []string(nil)
		if name == "codex" {
			want = []string{"codex app-server"}
		}
		if got := p.SessionDaemons(); !slices.Equal(got, want) {
			t.Errorf("%s daemons = %q, want %q", name, got, want)
		}
	}
	base := "name: my-agent\nimage: img:1\ncommand: run\nsession:\n  id_format: uuid\n  resume_argv: [\"{{command}}\", \"{{session_id}}\"]\n"
	if p, err := parseProfile([]byte(base+"  daemons: [\"run serve\", \"run-helper\"]\n"), "test.yaml", "my-agent"); err != nil ||
		!slices.Equal(p.SessionDaemons(), []string{"run serve", "run-helper"}) {
		t.Fatalf("valid daemons = %v, %v", p, err)
	}
	for _, bad := range []string{`""`, `"run  serve"`, `"/usr/bin/run serve"`, `"run;id"`, `"run $(id)"`, `"a b c d e f g h i"`} {
		if _, err := parseProfile([]byte(base+"  daemons: ["+bad+"]\n"), "test.yaml", "my-agent"); err == nil || !strings.Contains(err.Error(), "session.daemons") {
			t.Errorf("daemon %s = %v; want a refusal", bad, err)
		}
	}
}
