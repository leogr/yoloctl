package features

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentsession/observe"
)

func TestAgentObserveSettingsRegisterOnlyTheContractHooks(t *testing.T) {
	var settings struct {
		StatusLine struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"statusLine"`
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Async   bool   `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	decoder := json.NewDecoder(strings.NewReader(agentObserveSettingsJSON()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		t.Fatal(err)
	}
	var events []string
	for event, matchers := range settings.Hooks {
		events = append(events, event)
		if len(matchers) != 1 || len(matchers[0].Hooks) != 1 {
			t.Fatalf("%s: %+v", event, matchers)
		}
		h := matchers[0].Hooks[0]
		if h.Type != "command" || h.Command != agentObserveHook || !h.Async {
			t.Fatalf("%s hook must be async, so Claude never waits for it: %+v", event, h)
		}
	}
	slices.Sort(events)
	if !slices.Equal(events, []string{"Notification", "PostModelSwitch", "SessionEnd", "SessionStart", "Stop", "StopFailure", "UserPromptSubmit"}) {
		t.Fatalf("hook set: %v", events)
	}
	// Event-driven only: no refreshInterval or padding, nothing else set.
	if settings.StatusLine.Type != "command" || settings.StatusLine.Command != agentObserveStatusLine {
		t.Fatalf("status line: %+v", settings.StatusLine)
	}
}

func TestAgentObserveInstall(t *testing.T) {
	recipe, err := GenerateDockerfile("base", []string{"yolo.agent.observe"})
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(recipe, "<<'AGENT_OBSERVE_INSTALL'\n")
	if !ok {
		t.Fatal("missing build script")
	}
	script, _, ok := strings.Cut(rest, "\nAGENT_OBSERVE_INSTALL\n")
	if !ok || !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Fatal("script must have a shebang so BuildKit keeps the payload out of argv")
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid installation script: %v\n%s", err, out)
	}
	_, payload, _ := strings.Cut(script, "<<'AGENT_OBSERVE_SOURCE'\n")
	payload, _, _ = strings.Cut(payload, "AGENT_OBSERVE_SOURCE\n")
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload, "\n", ""))
	if err != nil || string(decoded) != agentObserveScript {
		t.Fatal("the writer is installed verbatim")
	}
	if !strings.HasSuffix(script, "\nprintf '%s\\n' '"+agentObserveSettingsJSON()+"' > "+agentObserveSettings) {
		t.Fatal("the drop-in must be written verbatim as one single-quoted word")
	}
	if !strings.Contains(script, `"exec $python -I -S /usr/local/lib/yoloctl/agent-observe.py" > `+agentObserveHook) ||
		!strings.Contains(script, `"exec $python -I -S /usr/local/lib/yoloctl/agent-observe.py statusline" > `+agentObserveStatusLine) ||
		!strings.Contains(script, "chmod 0755 "+agentObserveHook+" "+agentObserveStatusLine) || strings.Contains(script, "exit 0") {
		t.Fatal("the hook and the status line exec the interpreter resolved at build time (isolated, no site), with no silent fallback")
	}
	if strings.Contains(recipe, "/home/sandbox") || strings.Contains(recipe, "/etc/claude-code/managed-settings.json") {
		t.Fatal("the hook must not write the shared account leaf or replace the base managed settings")
	}
}

// The build resolves a working interpreter or fails; it never installs a hook
// that cannot run. The resolution part runs here against fake interpreters.
func TestAgentObserveInterpreterIsGuaranteedAtBuild(t *testing.T) {
	recipe, err := GenerateDockerfile("base", []string{"yolo.agent.observe"})
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(recipe, "<<'AGENT_OBSERVE_INSTALL'\n")
	resolve, _, ok := strings.Cut(rest, "install -d ")
	if !ok {
		t.Fatal("resolution must precede installation")
	}
	dir := t.TempDir()
	fake := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	run := func(local, system string) (string, error) {
		script := strings.NewReplacer("/usr/local/bin/python3", local, "/usr/bin/python3", system,
			"command -v apt-get", "command -v yoloctl-no-apt-get").Replace(resolve)
		out, err := exec.Command("sh", "-c", script+`printf '%s' "$python"`).CombinedOutput()
		return string(out), err
	}
	good, old, missing := fake("good", "exit 0"), fake("old", "exit 1"), filepath.Join(dir, "missing")
	for _, tc := range []struct{ local, system, want string }{
		{good, old, good},
		{old, good, good},
		{missing, good, good},
	} {
		if got, err := run(tc.local, tc.system); err != nil || got != tc.want {
			t.Errorf("resolve(%s, %s) = %q, %v; want %q", filepath.Base(tc.local), filepath.Base(tc.system), got, err, tc.want)
		}
	}
	if out, err := run(old, missing); err == nil || !strings.Contains(out, "needs Python 3.8+") {
		t.Fatalf("no usable interpreter and no apt-get must fail the build: %q %v", out, err)
	}
}

// The writer's bounds are the reader's: a mismatch would make an honest
// writer's files look tampered.
func TestAgentObserveWriterMatchesReaderBounds(t *testing.T) {
	for name, want := range map[string]int{"FILE_BYTES": observe.FileBytes, "LINE_BYTES": observe.LineBytes} {
		m := regexp.MustCompile(`(?m)^` + name + ` = (\d+)`).FindStringSubmatch(agentObserveScript)
		if m == nil {
			t.Fatalf("%s missing", name)
		}
		if got, _ := strconv.Atoi(m[1]); got != want {
			t.Errorf("%s = %d, reader expects %d", name, got, want)
		}
	}
	if !strings.Contains(agentObserveScript, "ROOT = '"+observe.ContainerDir+"'") ||
		!strings.Contains(agentObserveScript, "'"+observe.EnvGeneration+"'") {
		t.Fatal("the writer uses the bind target and generation variable the controller provides")
	}
	if !strings.Contains(agentObserveScript, "'event': '"+observe.StatusLine+"'") ||
		!strings.Contains(agentObserveScript, fmt.Sprintf("CONTEXT_TOKENS = 10 ** %d", 9)) || observe.ContextTokens != 1_000_000_000 {
		t.Fatal("the status line event name and context bound are the reader's")
	}
	m := regexp.MustCompile(`(?m)^STATES = \(([^)]*)\)`).FindStringSubmatch(agentObserveScript)
	if m == nil {
		t.Fatal("STATES missing")
	}
	var states []string
	for _, s := range strings.Split(m[1], ",") {
		states = append(states, strings.Trim(strings.TrimSpace(s), "'"))
	}
	for _, s := range []string{observe.StateWorking, observe.StateIdle, observe.StateWaitingPermission, observe.StateWaitingInput, observe.StateWaitingDialog, observe.StateFailed} {
		if !slices.Contains(states, s) {
			t.Errorf("the writer cannot write state %q", s)
		}
	}
	for _, s := range states {
		if !observe.ValidState(s) {
			t.Errorf("the writer writes state %q, which the reader refuses", s)
		}
	}
	m = regexp.MustCompile(`(?m)^EFFORTS = \(([^)]*)\)`).FindStringSubmatch(agentObserveScript)
	if m == nil {
		t.Fatal("EFFORTS missing")
	}
	var efforts []string
	for _, e := range strings.Split(m[1], ",") {
		efforts = append(efforts, strings.Trim(strings.TrimSpace(e), "'"))
	}
	for _, e := range efforts {
		if !observe.ValidEffort(e) {
			t.Errorf("the writer keeps effort %q, which the reader refuses", e)
		}
	}
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !slices.Contains(efforts, e) {
			t.Errorf("the writer drops effort %q, which the reader accepts", e)
		}
	}
}

// The agy feature installs the writer and its launcher only: no image-level
// agy settings exist, and the account leaf is never written at build time.
func TestAgentObserveAgyInstall(t *testing.T) {
	recipe, err := GenerateDockerfile("base", []string{"yolo.agent.observe-agy"})
	if err != nil {
		t.Fatal(err)
	}
	_, rest, _ := strings.Cut(recipe, "<<'AGENT_OBSERVE_INSTALL'\n")
	script, _, ok := strings.Cut(rest, "\nAGENT_OBSERVE_INSTALL\n")
	if !ok {
		t.Fatal("missing build script")
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid installation script: %v\n%s", err, out)
	}
	if !strings.Contains(script, `"exec $python -I -S /usr/local/lib/yoloctl/agent-observe.py agy" > `+AgentObserveAgyStatusLine) ||
		!strings.Contains(script, "chmod 0755 "+AgentObserveAgyStatusLine+"\n") {
		t.Fatal("the launcher execs the writer in agy mode with the interpreter resolved at build time")
	}
	if strings.Contains(recipe, "/home/sandbox") || strings.Contains(recipe, ".gemini") || strings.Contains(recipe, "/etc/") {
		t.Fatal("the build writes no agy or account settings")
	}
	// The agy launcher seeds the same path in a new account's settings.
	if !strings.Contains(agyLaunch, "'"+AgentObserveAgyStatusLine+"'") {
		t.Fatal("agy-launch.py must seed the feature's launcher path")
	}
}
