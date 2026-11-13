package agentbox

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

const (
	resultTargetID      = "1111111111111111111111111111111111111111111111111111111111111111"
	resultReplacementID = "2222222222222222222222222222222222222222222222222222222222222222"
)

// resultStopFixture is a fake engine: inspect by the target ID answers with
// the given labels and state, inspect by name answers with a replacement,
// and every command is logged.
func resultStopFixture(t *testing.T, targetJSON string) (*container.Client, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	directory := t.TempDir()
	log := filepath.Join(directory, "commands.log")
	t.Setenv("YOLOCTL_RESULT_STOP_LOG", log)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	replacement := `[{"Id":"` + resultReplacementID + `","Name":"coder","Config":{"Labels":{"yoloctl.agentbox":"coder","yoloctl.space":"fixture"}},"State":{"Status":"running","Running":true,"Pid":43}}]`
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$YOLOCTL_RESULT_STOP_LOG"
case "$*" in
 *"sudo nerdctl inspect ` + resultTargetID + `") printf '%s\n' '` + targetJSON + `' ;;
 *"sudo nerdctl inspect coder") printf '%s\n' '` + replacement + `' ;;
 *"sudo nerdctl stop --time 30 ` + resultTargetID + `") exit 0 ;;
 *) exit 97 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return container.NewWithOutput("fixture", io.Discard, io.Discard), log
}

func resultTargetJSON(name, space string, running bool) string {
	state := `{"Status":"exited","Running":false,"Pid":0}`
	if running {
		state = `{"Status":"running","Running":true,"Pid":42}`
	}
	return `[{"Id":"` + resultTargetID + `","Name":"` + name + `","Config":{"Labels":{"yoloctl.agentbox":"` + name + `","yoloctl.space":"` + space + `"}},"State":` + state + `}]`
}

func loggedCommands(t *testing.T, log string) string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

// The box was replaced between the stop's first read and its engine stop:
// the stop reads and stops the first container by its full ID only; the
// name, which now selects the replacement, is never resolved again.
func TestResultStopTargetsTheReadContainerNeverAReplacement(t *testing.T) {
	client, log := resultStopFixture(t, resultTargetJSON("coder", "fixture", true))
	err := LiveApplier{Client: client}.StopRuntimeForResult(t.Context(), "coder", resultTargetID)
	commands := loggedCommands(t, log)
	if err != nil || !strings.Contains(commands, "nerdctl stop --time 30 "+resultTargetID) ||
		strings.Contains(commands, resultReplacementID) || strings.Contains(commands, "nerdctl inspect coder\n") ||
		strings.Count(commands, "nerdctl stop") != 1 {
		t.Fatalf("%v\n%s", err, commands)
	}
}

// The ID now belongs to another box or space, or the target already
// stopped: no engine stop.
func TestResultStopRefusesAnotherOwnerAndSkipsAStoppedTarget(t *testing.T) {
	for label, target := range map[string]string{
		"other box":   resultTargetJSON("other", "fixture", true),
		"other space": resultTargetJSON("coder", "elsewhere", true),
		"stopped":     resultTargetJSON("coder", "fixture", false),
	} {
		client, log := resultStopFixture(t, target)
		err := LiveApplier{Client: client}.StopRuntimeForResult(t.Context(), "coder", resultTargetID)
		if commands := loggedCommands(t, log); strings.Contains(commands, "nerdctl stop") || (label == "stopped") != (err == nil) {
			t.Fatalf("%s: %v\n%s", label, err, commands)
		}
	}
	if err := (LiveApplier{Client: container.New("fixture")}).StopRuntimeForResult(t.Context(), "coder", "abc"); err == nil {
		t.Fatal("a partial ID was accepted as a target")
	}
}
