package main

import (
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// A Firecracker space's vm.log interleaves three producers: yoloctl's own
// records (the helper's and, over the serial console, the guest's), the
// Firecracker process's logger and the guest kernel's console. This
// presentation adapter labels only those identified formats; any other line,
// lifecycle markers included, is shown as the generic view shows it. The
// stored bytes are never changed, and debug log level still shows them as
// they are.

// firecrackerLogLine is Firecracker v1.17's logger line (src/vmm/src/logger/
// logging.rs): local time without a zone, then [instance:thread] or, with
// show_level, [instance:thread:LEVEL] and, with show_log_origin, :file:line.
// yoloctl does not configure the logger, so lines carry no level by default.
var firecrackerLogLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}) \[([^\]]*)\] (.*)$`)

// kernelConsoleLine is a printk line on the serial console: seconds since
// boot, then the message.
var kernelConsoleLine = regexp.MustCompile(`^\[ *(\d+\.\d{6})\] (.*)$`)

const firecrackerLogTime = "2006-01-02T15:04:05.000000000"

var firecrackerLogLevels = map[string]slog.Level{
	"ERROR": slog.LevelError, "WARN": slog.LevelWarn, "INFO": slog.LevelInfo, "DEBUG": slog.LevelDebug, "TRACE": slog.LevelDebug,
}

// newFirecrackerSpaceLogRecord returns the parser for one view of vm.log. A
// line without its own time (a kernel or marker line) takes the time of the
// last line that had one, so replayed history is not stamped with the time
// it is read.
func newFirecrackerSpaceLogRecord() func(string) slog.Record {
	var last time.Time
	return func(line string) slog.Record {
		line = strings.TrimRight(line, "\r") // serial console line ends
		if m := firecrackerLogLine.FindStringSubmatch(line); m != nil {
			if stamp, err := time.ParseInLocation(firecrackerLogTime, m[1], time.Local); err == nil {
				if record, ok := firecrackerRecord(stamp, m[2], m[3]); ok {
					last = stamp
					return record
				}
			}
		}
		if m := kernelConsoleLine.FindStringSubmatch(line); m != nil {
			record := slog.NewRecord(stampOr(last), slog.LevelInfo, "["+m[1]+"] "+m[2], 0)
			record.AddAttrs(prettylog.Actor(logging.VM), prettylog.Executor("kernel"))
			return record
		}
		record := spaceLogRecord(line)
		if first, _, ok := strings.Cut(line, " "); ok {
			if _, err := time.Parse(time.RFC3339Nano, first); err == nil {
				last = record.Time
				return record
			}
		}
		if !last.IsZero() {
			stamped := slog.NewRecord(last, record.Level, record.Message, 0)
			record.Attrs(func(a slog.Attr) bool { stamped.AddAttrs(a); return true })
			return stamped
		}
		return record
	}
}

// firecrackerRecord reads the bracketed instance/thread/level of a logger
// line; an unknown level keeps the line out of this format.
func firecrackerRecord(stamp time.Time, origin, message string) (slog.Record, bool) {
	parts := strings.Split(origin, ":")
	if len(parts) < 2 || parts[0] == "" {
		return slog.Record{}, false
	}
	level := slog.LevelInfo
	if len(parts) > 2 {
		known, ok := firecrackerLogLevels[parts[2]]
		if !ok {
			return slog.Record{}, false
		}
		level = known
	}
	record := slog.NewRecord(stamp, level, message, 0)
	record.AddAttrs(prettylog.Actor(logging.VM), prettylog.Executor(logging.Firecracker))
	return record, true
}

func stampOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
