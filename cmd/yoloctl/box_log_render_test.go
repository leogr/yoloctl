package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"golang.org/x/term"
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

// A box.logs request renders both streams. On a terminal request only a
// destination that is the raw terminal gets CRLF; a redirected one keeps LF.
func TestViewBoxLogs(t *testing.T) {
	lines := boxLogFixture(t)
	raw := []byte(strings.Join(lines[:3], "\r\n") + "\r\n")
	restore := logging.SetLogger(prettylog.New(&bytes.Buffer{}, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Normal}))
	defer restore()
	var out, redirected bytes.Buffer
	streams := controller.ClientIO{Out: &out, Err: &redirected}
	closeViews := viewBoxLogs(&streams, &out, &redirected, true)
	if _, err := streams.Err.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := closeViews(); err != nil {
		t.Fatal(err)
	}
	if text := redirected.String(); !strings.Contains(text, "box/openshell_supervisor: Starting sandbox supervision\n") || strings.Contains(text, "\r") {
		t.Fatalf("redirected view %q", text)
	}

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	state, err := term.MakeRaw(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = term.Restore(int(slave.Fd()), state) }()
	got := make(chan string, 1)
	go func() { b, _ := io.ReadAll(master); got <- string(b) }()
	streams = controller.ClientIO{Out: slave, Err: slave}
	closeViews = viewBoxLogs(&streams, slave, slave, true)
	if _, err := streams.Out.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := closeViews(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	var text string
	select {
	case text = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal view did not finish")
	}
	if !strings.Contains(text, "Starting sandbox supervision") || !strings.Contains(text, "\r\n") || strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Fatalf("terminal view %q", text)
	}
}

// Text that looks like a yoloctl record is still application output: no
// level, actor or outcome is read from it.
func TestBoxLogApplicationTextStaysText(t *testing.T) {
	parse := newBoxLogRecord()
	for _, line := range []string{"uncertain: box: deploy finished?", "error: host: not ours", "2026-11-05T20:44:42Z warning: vm: also not ours"} {
		r := parse(line)
		if r.Message != line || r.Level != slog.LevelInfo || recordAttr(r, prettylog.KeyActor) != "" || recordAttr(r, prettylog.KeyOutcome) != "" {
			t.Fatalf("%q read as %v %q actor %q", line, r.Level, r.Message, recordAttr(r, prettylog.KeyActor))
		}
	}
}
