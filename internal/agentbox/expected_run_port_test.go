package agentbox

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

// A port forward assumes the run the operation read or started: a
// replacement answering on the same name, or the same container in a new
// run, ends the operation before any forward command.
func TestPortForwardRefusesAnotherRun(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for label, now := range map[string]string{
		"replacement": `"Id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","State":{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-11-14T10:00:00.1Z"}`,
		"new run":     `"Id":"` + id + `","State":{"Status":"running","Running":true,"Pid":77,"StartedAt":"2026-11-14T10:05:00.2Z"}`,
		"stopped":     `"Id":"` + id + `","State":{"Status":"exited","Running":false,"Pid":0,"StartedAt":"2026-11-14T10:00:00.1Z"}`,
	} {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("LIMA_HOME", t.TempDir())
		dir := t.TempDir()
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("PORT_RUN_LOG", filepath.Join(dir, "log"))
		script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$PORT_RUN_LOG\"\ncase \"$*\" in\n *'sudo nerdctl inspect coder') printf '%s\\n' '[{\"Name\":\"coder\",\"Config\":{\"Labels\":{}}," + now + "}]' ;;\n *) exit 97 ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		ctx := container.WithExpectedRuns(t.Context())
		container.ExpectedRunsFrom(ctx).Expect("coder", container.Run{ID: id, StartedAt: "2026-11-14T10:00:00.1Z", PID: 42})
		a := LiveApplier{Client: container.NewWithOutput("fixture", io.Discard, io.Discard)}
		err := a.AddPortForward(ctx, &Config{Name: "coder", Space: "fixture"}, Port{Published: 8080, Target: 80})
		log, _ := os.ReadFile(filepath.Join(dir, "log"))
		var stopped *container.StoppedDuringOperation
		if err == nil || !strings.Contains(err.Error(), "stopped during the operation") || !errors.As(err, &stopped) ||
			strings.Count(string(log), "\n") != 1 {
			t.Fatalf("%s: %v\n%s", label, err, log)
		}
	}
}
