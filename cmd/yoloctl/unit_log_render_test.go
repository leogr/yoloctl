package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Owned records keep their level, actor and time; child output, however it
// begins, stays literal text at the last record's time.
func TestUnitLogRecords(t *testing.T) {
	parse := newUnitLogRecord()
	owned := parse("2026-11-14T10:00:00.5Z warning: session: holder lost its child")
	if owned.Level != slog.LevelWarn || owned.Message != "holder lost its child" || recordAttr(owned, prettylog.KeyActor) != logging.Session {
		t.Fatalf("owned record %v %q", owned.Level, owned.Message)
	}
	for _, line := range []string{
		"debug: child chatter that must stay visible",
		"uncertain: box: not ours",
		"error: host: not ours either",
		"2026-11-14T10:00:01Z uncertain: something: child text with a time",
		"plain child output",
	} {
		r := parse(line)
		if r.Message != line || r.Level != slog.LevelInfo || recordAttr(r, prettylog.KeyActor) != "" || recordAttr(r, prettylog.KeyOutcome) != "" ||
			!r.Time.Equal(time.Date(2026, 11, 14, 10, 0, 0, 5e8, time.UTC)) {
			t.Fatalf("%q read as %v %q actor %q at %v", line, r.Level, r.Message, recordAttr(r, prettylog.KeyActor), r.Time)
		}
	}
}
