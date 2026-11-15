package prettylog

import (
	"io"
	"strconv"
	"testing"
)

// Retained exposes every kept line, not only the last TailLines, and counts
// only the bytes the limits dropped.
func TestTrackerRetainedKeepsAllLines(t *testing.T) {
	tr := NewTracker(TrackerLimits{StepDetail: 64})
	l := New(io.Discard, Options{Format: FormatPretty, Verbosity: Trace})
	l.Route(io.Discard, tr)
	s := l.StartStep("work")
	for i := range 30 {
		_, _ = io.WriteString(s.Detail(), "l"+strconv.Itoa(i)+"\n")
	}
	got := tr.Retained()
	if len(got) != 1 || len(got[0].Tail) <= TailLines && got[0].Omitted == 0 {
		t.Fatalf("retained: %+v", got)
	}
	if steps := tr.Steps(); len(steps[0].Tail) > TailLines {
		t.Fatal("Steps still shows only the tail")
	}
	full := NewTracker(TrackerLimits{})
	lf := New(io.Discard, Options{Format: FormatPretty, Verbosity: Trace})
	lf.Route(io.Discard, full)
	sf := lf.StartStep("work")
	for i := range 30 {
		_, _ = io.WriteString(sf.Detail(), "l"+strconv.Itoa(i)+"\n")
	}
	if r := full.Retained(); len(r[0].Tail) != 30 || r[0].Omitted != 0 || len(full.Steps()[0].Tail) != TailLines {
		t.Fatalf("within the limits every line is retained: %d lines, omitted %d", len(r[0].Tail), r[0].Omitted)
	}
	all := got[0].Tail
	if all[len(all)-1] != "l29" || got[0].Omitted <= 0 {
		t.Fatalf("the limit drops the oldest lines and counts them: %v omitted=%d", all, got[0].Omitted)
	}
}
