package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func hostViewFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	path, err := layout.HostDiagnosticsLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("long diagnostic ", 150) + "TAIL\n"
	legacy := "2026-11-14T19:30:00Z \"old global\" \"legacy\"\n"
	current := "2026-11-14T19:31:00.123Z \"limactl start\" " + strconv.QuoteToASCII("warning\n\x1b]52;c;no\a\n"+long) + " space=\"alpha\"\n"
	if err := os.WriteFile(path, []byte(legacy+current), 0600); err != nil {
		t.Fatal(err)
	}
	return legacy + current, current
}

func TestHostLogViewPrettyCompleteEscapedAndDebugExact(t *testing.T) {
	all, alpha := hostViewFixture(t)
	for _, tc := range []struct{ level, space, want string }{{"debug", "", all}, {"debug", "alpha", alpha}, {"warn", "", ""}, {"error", "alpha", ""}} {
		var out bytes.Buffer
		err := tailHostLogWithSettings(t.Context(), tc.space, 100, false, &out, spaceLogSettings(t, tc.level))
		if err != nil || out.String() != tc.want {
			t.Fatalf("%+v: %v output %q", tc, err, out.String())
		}
	}
	var out bytes.Buffer
	if err := tailHostLogWithSettings(t.Context(), "alpha", 100, false, &out, spaceLogSettings(t, "info")); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"host", "limactl start", "space=alpha", "warning", "TAIL"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if strings.ContainsRune(text, '\x1b') || strings.Contains(text, "old global") || strings.Contains(text, "(+") {
		t.Fatal("lost or unsafe diagnostic body", text)
	}
	if err := tailHostLogWithSettings(t.Context(), "", 1, false, failingWriter{}, spaceLogSettings(t, "info")); err == nil {
		t.Fatal("discarded writer failure")
	}
}

func TestHostLogUnknownTextHasNoOutcome(t *testing.T) {
	line := "uncertain: box: arbitrary application text\n"
	r := hostLogRecord(line)
	s := prettylog.StructureOf(r)
	if r.Message != strings.TrimSuffix(line, "\n") || s.Outcome != "" || s.Actor != logging.Host || r.Level != slog.LevelInfo {
		t.Fatal(r, s)
	}
}

func TestSpaceLogsHostDoesNotResolveDefaultOrContactVM(t *testing.T) {
	hostViewFixture(t)
	t.Setenv("YOLOCTL_SPACE", "invalid space pin")
	cmd := newSpaceLogsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--host"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "old global") || !strings.Contains(out.String(), "limactl start") {
		t.Fatal(out.String())
	}
	cmd = newSpaceLogsCmd()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"removed-space", "--host"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("invented context", out.String())
	}
	// The independently built --unit addition joins this command at composition.
	cmd = newSpaceLogsCmd()
	if cmd.Flags().Lookup("unit") == nil {
		cmd.Flags().String("unit", "", "unit view")
	}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--host", "--unit", "service"})
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatal(err)
	}
}

func TestHostLogReaderErrorReachesCommand(t *testing.T) {
	t.Setenv("YOLOCTL_HOME", t.TempDir())
	cmd := newSpaceLogsCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--host"})
	if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
