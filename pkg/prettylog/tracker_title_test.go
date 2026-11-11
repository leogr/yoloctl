package prettylog

import (
	"log/slog"
	"testing"
	"time"
)

// An end state may retitle its step ("… ready"); a running record does not.
func TestTrackerEndStateRetitlesItsStep(t *testing.T) {
	tr := NewTracker(TrackerLimits{})
	rec := func(msg, state string) slog.Record {
		r := slog.NewRecord(time.Now(), LevelInfo, msg, 0)
		r.AddAttrs(slog.String(KeyStep, "s"), slog.String(KeyState, state))
		return r
	}
	tr.Apply(rec("Image for box ok1", "running"))
	tr.Apply(rec("ignored", "running"))
	if got := tr.Steps()[0].Title; got != "Image for box ok1" {
		t.Fatal(got)
	}
	tr.Apply(rec("Image for box ok1 ready", "ok"))
	if got := tr.Steps()[0].Title; got != "Image for box ok1 ready" {
		t.Fatal(got)
	}
}
