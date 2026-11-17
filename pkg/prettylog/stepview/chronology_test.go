package stepview

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestCompletedChildStaysBeforeLaterOutput(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Starting space work", prettylog.Actor("host"))
	child := root.Step("Started controller")
	child.Done()
	l.Slog().Info("Restarting box one")
	got := scr.text()
	if strings.Count(got, "Started controller") != 1 || strings.Index(got, "Started controller") > strings.Index(got, "Restarting box one") {
		t.Fatalf("completion moved below later output:\n%s", got)
	}
	v.mu.Lock()
	v.updateLocked(false)
	v.mu.Unlock()
	root.Done()
	v.Close()
	got = scr.text()
	if strings.Count(got, "Started controller") != 1 || strings.Count(got, "Starting space work") != 1 || strings.ContainsAny(got, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("stale or repeated rows:\n%s", got)
	}
}

func TestChildCompletionOrderAndFailureTailAreCommittedOnce(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Space parent", prettylog.Actor("host"))
	first := root.Step("First child")
	second := root.Step("Second child")
	nested := first.Step("Nested failure")
	v.Suspend()
	before := scr.raw.String()
	second.Done()
	_, _ = io.WriteString(nested.Detail(), "failure final detail")
	nested.Fail(errors.New("tool failed"))
	first.Fail(errors.New("child failed"))
	if scr.raw.String() != before {
		t.Fatal("completion printed while suspended")
	}
	v.Resume()
	l.Slog().Info("later notice")
	root.Fail(errors.New("space failed"))
	got := scr.text()
	for _, word := range []string{"Second child", "Nested failure", "First child", "failure final detail", "tool failed", "child failed"} {
		if strings.Count(got, word) != 1 {
			t.Fatalf("lost or duplicated %q:\n%s", word, got)
		}
	}
	if strings.Index(got, "Second child") >= strings.Index(got, "Nested failure") || strings.Index(got, "Nested failure") >= strings.Index(got, "First child") || strings.Index(got, "First child") >= strings.Index(got, "later notice") {
		t.Fatalf("completion order changed:\n%s", got)
	}
}

func TestPartialLineDefersChildCompletionUntilNewline(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Parent", prettylog.Actor("host"))
	child := root.Step("Child complete")
	w := v.Writer(scr)
	_, _ = io.WriteString(w, "partial")
	before := scr.raw.String()
	child.Done()
	if scr.raw.String() != before {
		t.Fatal("completion was printed mid-line")
	}
	_, _ = io.WriteString(w, " line\n")
	root.Done()
	got := scr.text()
	if !strings.HasPrefix(got, "partial line\n") || strings.Count(got, "Child complete") != 1 {
		t.Fatalf("partial-line completion: %s", got)
	}
}

func TestSnapshotDoesNotInventChildCompletionOrder(t *testing.T) {
	source := prettylog.New(io.Discard, prettylog.Options{Verbosity: prettylog.Trace})
	tracker := prettylog.NewTracker(prettylog.TrackerLimits{})
	restore := source.Route(io.Discard, tracker)
	root := source.StartStep("Restored parent", prettylog.Actor("host"))
	root.Step("Historical successful child").Done()
	restore()
	scr := &screen{}
	v := New(scr, Options{Interval: time.Hour})
	defer v.Close()
	for _, r := range tracker.Snapshot() {
		v.Apply(r)
	}
	end := slog.NewRecord(time.Now(), prettylog.LevelDebug, "snapshot done", 0)
	end.AddAttrs(slog.Bool(prettylog.KeySnapshot, true))
	v.Apply(end)
	if strings.Contains(scr.text(), "Historical successful child") {
		t.Fatal("snapshot invented chronological child output", scr.text())
	}
	restore = source.Route(io.Discard, v)
	defer restore()
	root.Step("New live completion").Done()
	root.Done()
	if got := scr.text(); strings.Contains(got, "Historical successful child") || strings.Count(got, "New live completion") != 1 {
		t.Fatal(got)
	}
}

func TestUnknownChildTailSurvivesCloseOnce(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Parent unfinished", prettylog.Actor("host"))
	child := root.Step("Uncertain child")
	for i := range 25 {
		_, _ = fmt.Fprintf(child.Detail(), "kept detail %d\n", i)
	}
	child.Unknown(errors.New("unknown receipt"))
	v.Close()
	got := scr.text()
	for _, text := range []string{"Uncertain child", "unknown receipt", "kept detail 24", "earlier bytes omitted", unfinished} {
		if strings.Count(got, text) != 1 {
			t.Fatalf("lost/duplicated %q: %s", text, got)
		}
	}
	if strings.Contains(got, "kept detail 4\n") {
		t.Fatal("expanded beyond the existing tail", got)
	}
}

func TestOrphanAndCyclicChildRecordsKeepNoScrollbackOwnership(t *testing.T) {
	l, _, scr, _ := setup(t, nil)
	root := l.StartStep("Known root", prettylog.Actor("host"))
	for _, tc := range []struct{ id, parent string }{{"orphan-child", "absent"}, {"cyclic-child", "cyclic-child"}} {
		r := slog.NewRecord(time.Now(), prettylog.LevelInfo, "Invalid child", 0)
		r.AddAttrs(slog.String(prettylog.KeyStep, tc.id), slog.String(prettylog.KeyParent, tc.parent), slog.String(prettylog.KeyState, "ok"))
		_ = l.Handle(t.Context(), r)
	}
	root.Done()
	if strings.Contains(scr.text(), "Invalid child") {
		t.Fatal("orphan/cyclic child was published", scr.text())
	}
}

func TestRestoredFailureKeepsDepthBelowAnAlreadyCommittedParent(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Root", prettylog.Actor("host"))
	parent := root.Step("Committed failed parent")
	parent.Fail(errors.New("parent failure"))
	restored := slog.NewRecord(time.Now(), prettylog.LevelError, "Restored nested failure", 0)
	restored.AddAttrs(slog.String(prettylog.KeyStep, "restored-child"), slog.String(prettylog.KeyParent, parent.ID()), slog.String(prettylog.KeyState, "failed"), slog.String(prettylog.KeyErr, "nested failure"), slog.Bool(prettylog.KeySnapshot, true))
	v.Apply(restored)
	end := slog.NewRecord(time.Now(), prettylog.LevelDebug, "snapshot done", 0)
	end.AddAttrs(slog.Bool(prettylog.KeySnapshot, true))
	v.Apply(end)
	root.Fail(errors.New("root failure"))
	got := scr.text()
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "Restored nested failure") {
			if !strings.HasPrefix(line, "    ✗") {
				t.Fatalf("restored child lost its parent depth: %q\n%s", line, got)
			}
			return
		}
	}
	t.Fatal("restored nested failure lost", got)
}
