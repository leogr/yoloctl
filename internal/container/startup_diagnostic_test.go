package container

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startupDiagnosticFixture(t *testing.T, state, logs string) (*Client, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("YOLOCTL_TEST_STARTUP_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$YOLOCTL_TEST_STARTUP_LOG\"\ncase \"$*\" in\n" +
		"*'nerdctl inspect '*) " + state + ";;\n*'nerdctl logs '*) " + logs + ";;\n*) exit 79;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return NewWithOutput("fixture", io.Discard, io.Discard), log
}

func TestStartupFailureDiagnosticsActualCommandsKeepCIDAndHelperStderr(t *testing.T) {
	c, log := startupDiagnosticFixture(t, "printf 'exited exit=1 pid=0\\n'", "printf 'OpenShell helper: fixture failure\\033[31m\\n' >&2")
	id := strings.Repeat("a", 64)
	got := c.StartupFailureDiagnostics(t.Context(), id)
	if !strings.Contains(got, "exited exit=1 pid=0") || !strings.Contains(got, "OpenShell helper: fixture failure\\x1b") || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("diagnostic=%q", got)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "shell yolo-fixture -- sudo nerdctl inspect --mode dockercompat --format {{.State.Status}} exit={{.State.ExitCode}} pid={{.State.Pid}} " + id + "\n" +
		"shell yolo-fixture -- sudo nerdctl logs --tail 16 " + id + "\n"
	if string(raw) != want {
		t.Fatalf("commands=%q want=%q", raw, want)
	}
}

func TestStartupFailureDiagnosticsCaptureFailureIsExplicit(t *testing.T) {
	c, _ := startupDiagnosticFixture(t, "printf 'inspect unavailable' >&2; exit 23", "printf 'OpenShell helper: retained error' >&2")
	got := c.StartupFailureDiagnostics(t.Context(), strings.Repeat("b", 64))
	if !strings.Contains(got, "capture failed: exit status 23") || !strings.Contains(got, "retained error") {
		t.Fatal(got)
	}
}

func TestStartupFailureDiagnosticsRejectsAliasesWithoutCommands(t *testing.T) {
	c, log := startupDiagnosticFixture(t, "exit 0", "exit 0")
	for _, id := range []string{"", "name", strings.Repeat("A", 64), strings.Repeat("a", 12), strings.Repeat("a", 64) + "\n"} {
		if got := c.StartupFailureDiagnostics(t.Context(), id); !strings.Contains(got, "unavailable") {
			t.Fatal(got)
		}
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid target reached command: %v", err)
	}
}

func TestStartupFailureDiagnosticsBoundsActualOutput(t *testing.T) {
	c, _ := startupDiagnosticFixture(t, "printf 'exited exit=1 pid=0'", "i=0; while [ $i -lt 5000 ]; do printf '0123456789abcdef'; i=$((i+1)); done")
	got := c.StartupFailureDiagnostics(t.Context(), strings.Repeat("c", 64))
	if !strings.Contains(got, "[truncated]") || len(got) > startupDiagnosticLogLimit+1500 {
		t.Fatalf("size=%d diagnostic=%q", len(got), got)
	}
}

func TestStartupFailureDiagnosticsBoundsActualHang(t *testing.T) {
	c, _ := startupDiagnosticFixture(t, "printf 'exited exit=1 pid=0'", "exec sleep 10")
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // optional capture retains the owned CID after original failure
	start := time.Now()
	got := c.StartupFailureDiagnostics(ctx, strings.Repeat("d", 64))
	if !strings.Contains(got, "capture failed") || time.Since(start) > 6*time.Second {
		t.Fatalf("elapsed=%v diagnostic=%q", time.Since(start), got)
	}
}
