package main

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// The fixture is real `box logs` output of an OpenShell box (startup, a
// shutdown panic report, a finding), with a neutral cargo path.
func boxLogFixture(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("testdata/box-logs-openshell.txt")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func TestBoxLogRecords(t *testing.T) {
	parse := newBoxLogRecord()
	var records []slog.Record
	for _, line := range boxLogFixture(t) {
		records = append(records, parse(line))
	}
	first := records[0]
	if first.Level != slog.LevelWarn || first.Message != "Could not open /var/log for log rotation; using stderr-only logging" ||
		!first.Time.Equal(time.Date(2026, 11, 5, 20, 44, 42, 925e6, time.UTC)) ||
		recordAttr(first, prettylog.KeyActor) != logging.Box || recordAttr(first, prettylog.KeyExecutor) != "openshell_supervisor" {
		t.Fatalf("tracing record %v %v %q", first.Time, first.Level, first.Message)
	}
	byMessage := map[string]slog.Record{}
	for _, r := range records {
		byMessage[r.Message] = r
	}
	for message, want := range map[string]slog.Level{
		"CONFIG:LOADING Loading OPA policy engine from local files [rules:/.openshell/supervisor/config/sandbox-policy.rego data:/.openshell/supervisor/config/policy.yaml]": slog.LevelInfo,
		"SSH:LISTEN":       slog.LevelInfo,
		"SSH:FAIL":         slog.LevelInfo,
		"SSH:OPEN ALLOWED": slog.LevelInfo,
		`FINDING:CREATE "Sandbox Supervisor Connection Lost" [type:sandbox-supervisor-connection-lost confidence:high]`: slog.LevelWarn,
	} {
		r, ok := byMessage[message]
		if !ok || r.Level != want || recordAttr(r, prettylog.KeyExecutor) != "ocsf" {
			t.Fatalf("OCSF %q: found %v level %v", message, ok, r.Level)
		}
	}
	// The panic report is text at the time of the line before it.
	panicLine := byMessage["A Tokio 1.x context was found, but it is being shutdown."]
	if panicLine.Level != slog.LevelInfo || recordAttr(panicLine, prettylog.KeyActor) != "" ||
		!panicLine.Time.Equal(time.Date(2026, 11, 5, 21, 29, 5, 696e6, time.UTC)) {
		t.Fatalf("unstamped line %v %v", panicLine.Time, panicLine.Level)
	}
}

// Another OCSF severity, or a line that only looks stamped, stays text.
func TestBoxLogUnknownLinesStayText(t *testing.T) {
	parse := newBoxLogRecord()
	for _, line := range []string{
		"2026-11-05T20:44:42.925Z OCSF FINDING:CREATE [SEVERE] something",
		"2026-11-05T20:44:42.925Z NOTICE app: hello",
		"application output",
	} {
		if r := parse(line); recordAttr(r, prettylog.KeyActor) != "" || !strings.Contains(r.Message, strings.Fields(line)[len(strings.Fields(line))-1]) {
			t.Fatalf("%q read as %q by %q", line, r.Message, recordAttr(r, prettylog.KeyActor))
		}
	}
}

// A box.logs request renders both streams; on a raw terminal its lines end
// in CRLF; debug keeps the bytes.
func TestViewBoxLogs(t *testing.T) {
	lines := boxLogFixture(t)
	raw := []byte(strings.Join(lines[:3], "\r\n") + "\r\n")
	restore := logging.SetLogger(prettylog.New(&bytes.Buffer{}, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal}))
	defer restore()
	for _, terminal := range []bool{false, true} {
		var out, errOut bytes.Buffer
		streams := controller.ClientIO{Out: &out, Err: &errOut}
		closeViews := viewBoxLogs(&streams, &out, &errOut, terminal)
		if _, err := streams.Err.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := closeViews(); err != nil {
			t.Fatal(err)
		}
		text := errOut.String()
		if !strings.Contains(text, "box/openshell_supervisor: Starting sandbox supervision") || strings.Contains(text, "openshell_supervisor: Starting sandbox supervision\r\r") ||
			strings.Contains(text, `\x0d`) || terminal != strings.Contains(text, "\r\n") {
			t.Fatalf("terminal %v view %q", terminal, text)
		}
	}
}
