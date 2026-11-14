package main

import (
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// `box logs` replays a box container's stdout/stderr. In a box it is
// OpenShell's supervisor and sandbox output (formats taken from a real
// capture, tasks/feat-prettylog-remaining/item4-sample): Rust tracing lines,
// OCSF event lines and unstamped stderr such as a panic report. Only those two
// stamped formats are read; every other line, application output included,
// is shown as text. --log-level debug keeps the original bytes.

// boxTracingLine is `<RFC3339 UTC>Z <LEVEL> <crate::module>: <message>`.
var boxTracingLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z) (ERROR|WARN|INFO|DEBUG|TRACE) ([A-Za-z0-9_]+)((?:::[A-Za-z0-9_]+)*): (.*)$`)

// boxOCSFLine is `<RFC3339 UTC>Z OCSF <CLASS:ACTIVITY> [<SEVERITY>] <message>`;
// the message may be empty.
var boxOCSFLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z) OCSF ([A-Z_]+:[A-Z_]+) \[([A-Z]+)\](?: (.*))?$`)

var boxTracingLevels = map[string]slog.Level{
	"ERROR": slog.LevelError, "WARN": slog.LevelWarn, "INFO": slog.LevelInfo, "DEBUG": slog.LevelDebug, "TRACE": slog.LevelDebug,
}

// boxOCSFSeverities are the severities the capture shows; another severity
// keeps the line as text rather than guess its weight.
var boxOCSFSeverities = map[string]slog.Level{"INFO": slog.LevelInfo, "LOW": slog.LevelInfo, "MED": slog.LevelWarn}

// newBoxLogRecord returns the parser for one box log stream. A line without
// its own time takes the time of the last stamped line.
func newBoxLogRecord() func(string) slog.Record {
	var last time.Time
	return func(line string) slog.Record {
		line = strings.TrimRight(line, "\r") // a terminal request's PTY line ends
		if m := boxTracingLine.FindStringSubmatch(line); m != nil {
			if stamp, err := time.Parse(time.RFC3339Nano, m[1]); err == nil {
				last = stamp
				record := slog.NewRecord(stamp, boxTracingLevels[m[2]], m[5], 0)
				record.AddAttrs(prettylog.Actor(logging.Box), prettylog.Executor(m[3]))
				return record
			}
		}
		if m := boxOCSFLine.FindStringSubmatch(line); m != nil {
			level, known := boxOCSFSeverities[m[3]]
			if stamp, err := time.Parse(time.RFC3339Nano, m[1]); err == nil && known {
				last = stamp
				message := m[2]
				if m[4] != "" {
					message += " " + m[4]
				}
				record := slog.NewRecord(stamp, level, message, 0)
				record.AddAttrs(prettylog.Actor(logging.Box), prettylog.Executor("ocsf"))
				return record
			}
		}
		record := spaceLogRecord(line)
		if first, _, ok := strings.Cut(line, " "); ok {
			if _, err := time.Parse(time.RFC3339Nano, first); err == nil {
				last = record.Time
				return record
			}
		}
		if last.IsZero() {
			return record
		}
		stamped := slog.NewRecord(last, record.Level, record.Message, 0)
		record.Attrs(func(a slog.Attr) bool { stamped.AddAttrs(a); return true })
		return stamped
	}
}

// viewBoxLogs renders a box.logs request's output and error streams through
// the box log view, unless debug keeps the original bytes. On a raw terminal
// (a terminal request) the rendered lines end in CRLF. Each view still
// reports its writer as the terminal, so it is formatted for it. The returned
// function flushes both views.
func viewBoxLogs(streams *controller.ClientIO, stdout, stderr io.Writer, raw bool) func() error {
	settings := logging.CurrentSettings()
	if settings.Verbosity == prettylog.Trace {
		return func() error { return nil }
	}
	out := newLogView(boxLogTarget(streams.Out, stdout, raw), settings, newBoxLogRecord())
	errView := newLogView(boxLogTarget(streams.Err, stderr, raw), settings, newBoxLogRecord())
	streams.Out, streams.Err = out, errView
	return func() error { return errors.Join(out.Close(), errView.Close()) }
}

func boxLogTarget(w, terminal io.Writer, raw bool) io.Writer {
	if raw {
		w = &rawTerminalStderr{out: w}
	}
	if f, ok := terminal.(interface{ Fd() uintptr }); ok && logging.IsTerminal(terminal) {
		return terminalWriter{Writer: w, fd: f.Fd()}
	}
	return w
}

// terminalWriter is a writer that reaches the terminal fd through adapters.
type terminalWriter struct {
	io.Writer
	fd uintptr
}

func (w terminalWriter) Fd() uintptr { return w.fd }
