package main

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func attachControllerBridgeStderr(ctx context.Context, bridge *exec.Cmd, req controller.Request, streams controller.ClientIO) func() {
	// The terminal frontend provides a log sink for user-facing attach
	// output. Other bridge destinations include private diagnostic tails.
	if req.Method != "box.attach" || streams.Log == nil || filepath.Base(bridge.Path) != lima.DefaultBinary {
		return lima.AttachFilteredStderr(bridge, streams.Err)
	}
	logger := logging.Logger()
	return lima.AttachLoggedStderr(bridge, streams.Err, func(line string) bool {
		record, ok := limaControllerLogRecord(line)
		if ok {
			_ = logger.Handle(ctx, record)
		}
		return ok
	})
}

// limaControllerLogRecord accepts Lima's default non-terminal logrus format.
// Plain SSH diagnostics share the pipe, so require a complete record before
// consuming a line.
func limaControllerLogRecord(line string) (slog.Record, bool) {
	line = strings.TrimRight(line, "\r\n")
	text, ok := strings.CutPrefix(line, "time=")
	if !ok {
		return slog.Record{}, false
	}
	stamp, text, ok := limaLogValue(text)
	if !ok {
		return slog.Record{}, false
	}
	instant, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return slog.Record{}, false
	}
	severity, body, ok := strings.Cut(text, " msg=")
	if !ok {
		return slog.Record{}, false
	}
	var level slog.Level
	switch severity {
	case "level=trace", "level=debug":
		level = slog.LevelDebug
	case "level=info":
		level = slog.LevelInfo
	case "level=warning", "level=warn":
		level = slog.LevelWarn
	case "level=error", "level=fatal", "level=panic":
		level = slog.LevelError
	default:
		return slog.Record{}, false
	}
	message, details, ok := limaLogValue(body)
	if !ok || !validLimaLogFields(details) {
		return slog.Record{}, false
	}
	if details != "" {
		message += " " + details
	}
	record := slog.NewRecord(instant, level, message, 0)
	record.AddAttrs(prettylog.Actor(logging.VM), prettylog.Executor("lima"))
	return record, true
}

func validLimaLogFields(text string) bool {
	for text != "" {
		key, value, ok := strings.Cut(text, "=")
		if !ok || key == "" || strings.ContainsAny(key, " \"\t\r\n") {
			return false
		}
		_, text, ok = limaLogValue(value)
		if !ok {
			return false
		}
	}
	return true
}

func limaLogValue(text string) (value, rest string, ok bool) {
	if !strings.HasPrefix(text, `"`) {
		value, rest, _ = strings.Cut(text, " ")
		return value, rest, !strings.ContainsAny(value, "\"\t\r\n")
	}
	quoted, err := strconv.QuotedPrefix(text)
	if err != nil {
		return "", "", false
	}
	value, err = strconv.Unquote(quoted)
	if err != nil {
		return "", "", false
	}
	rest = text[len(quoted):]
	if rest == "" {
		return value, "", true
	}
	rest, ok = strings.CutPrefix(rest, " ")
	return value, rest, ok
}
