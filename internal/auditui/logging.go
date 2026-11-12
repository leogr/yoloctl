package auditui

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// eventLevel keeps Falco's priority word in the record while mapping its
// presentation to the shared log kinds. It never interprets an alert as a
// command outcome (which would bypass the ordinary consecutive-fold rule).
func eventLevel(priority string) slog.Level {
	switch strings.ToLower(priority) {
	case "emergency", "alert", "critical", "error":
		return prettylog.LevelError
	case "warning":
		return prettylog.LevelWarn
	case "notice":
		return prettylog.LevelNotice
	case "debug":
		return prettylog.LevelDebug
	default:
		return prettylog.LevelInfo
	}
}

// eventOrigin uses retained evidence only, matching the existing view. An
// unavailable label join must never borrow the CLI's selected space or box.
func eventOrigin(e audit.Event) string {
	if e.MetadataAvailable {
		return audit.Clean(e.Box)
	}
	switch e.Container {
	case "host":
		return "(vm)"
	case "":
		return "(unknown)"
	default:
		return "(no box)"
	}
}

// eventKey is the approved Falco identity, independent of time/cursor and
// unselected fields. JSON array framing distinguishes adjacent empty strings,
// separators and escaped controls without inventing a provenance field.
func eventKey(e audit.Event) string {
	encoded, _ := json.Marshal([]string{
		strings.ToLower(audit.Clean(e.Severity)), audit.Clean(e.Rule), eventOrigin(e),
		audit.Clean(e.Executable), audit.Clean(e.Result), audit.Clean(e.Path), audit.Clean(e.Endpoint),
	})
	return string(encoded)
}

// eventRecord is a presentation projection of already-retained fields. Raw
// Falco output, argv, environment, buffers and aggregate labels never enter it.
// Storage, JSON output and export retain the original event unchanged.
func eventRecord(e audit.Event) slog.Record {
	r := slog.NewRecord(e.Time, eventLevel(e.Severity), audit.Clean(e.Rule), 0)
	r.AddAttrs(prettylog.Actor(logging.Audit), prettylog.Executor(logging.Falco),
		slog.String("priority", audit.Clean(e.Severity)), slog.String("origin", eventOrigin(e)))
	fields := audit.SelectedFields(e)
	keys := make([]string, 0, len(fields))
	for key, value := range fields {
		if value != "" && value != "<NA>" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		r.AddAttrs(slog.String(key, fields[key]))
	}
	return r
}
