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

func TestCompletedChildStaysInLiveBlockUntilParentEnds(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Starting space work", prettylog.Actor("host"))
	child := root.Step("Started controller")
	child.Done()
	if got := scr.text(); !strings.Contains(got, "  ✓ Started controller") {
		t.Fatalf("child completion missing from the live block:\n%s", got)
	}
	l.Slog().Info("Restarting box one")
	got := scr.text()
	if strings.Count(got, "Started controller") != 1 || strings.Index(got, "Restarting box one") > strings.Index(got, "Starting space work") || strings.Index(got, "Starting space work") > strings.Index(got, "Started controller") {
		t.Fatalf("live rows missing or out of order:\n%s", got)
	}
	v.mu.Lock()
	v.updateLocked(false)
	v.mu.Unlock()
	root.Done()
	v.Close()
	got = scr.text()
	if strings.Count(got, "Started controller") != 1 || strings.Count(got, "Starting space work") != 1 || strings.Index(got, "Starting space work") > strings.Index(got, "Started controller") || strings.ContainsAny(got, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
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
	root.Fail(errors.New("space failed"))
	if scr.raw.String() != before {
		t.Fatal("completion printed while suspended")
	}
	v.Resume()
	got := scr.text()
	for _, word := range []string{"Second child", "Nested failure", "First child", "failure final detail", "tool failed", "child failed"} {
		if strings.Count(got, word) != 1 {
			t.Fatalf("lost or duplicated %q:\n%s", word, got)
		}
	}
	if strings.Index(got, "Space parent") > strings.Index(got, "Second child") || strings.Index(got, "Second child") > strings.Index(got, "First child") || strings.Index(got, "First child") > strings.Index(got, "Nested failure") || !strings.Contains(got, "\n    ✗ Nested failure") {
		t.Fatalf("failed group is out of order or misindented:\n%s", got)
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
	root.Done()
	if scr.raw.String() != before {
		t.Fatal("completion was printed mid-line")
	}
	_, _ = io.WriteString(w, " line\n")
	got := scr.text()
	if !strings.HasPrefix(got, "partial line\n") || strings.Count(got, "Child complete") != 1 || strings.Index(got, "Parent") > strings.Index(got, "Child complete") {
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
	if strings.Index(got, "Uncertain child") > strings.Index(got, "Parent unfinished") {
		t.Fatal("uncertain child must print before its unfinished parent closes", got)
	}
}

func TestFailedChildrenPrintWithTheirTailWhileRunningSiblingsFillTheBlock(t *testing.T) {
	l, _, scr, _ := setup(t, nil)
	root := l.StartStep("Upgrading space")
	failed := root.Step("Pulling image")
	unknown := root.Step("Checking receipt")
	for i := range maxChildren {
		root.Step(fmt.Sprintf("Running sibling %d", i))
	}
	_, _ = io.WriteString(failed.Detail(), "pull diagnostics\n")
	failed.Fail(errors.New("image pull refused"))
	unknown.Unknown(errors.New("receipt unavailable"))
	before := scr.text()
	root.Step("Skipped child").Skip("already ready")
	root.Done()
	after := scr.text()
	want := []string{"✗ Pulling image", "image pull refused", "pull diagnostics", "? Checking receipt", "receipt unavailable"}
	for _, got := range []string{before, after} {
		for _, text := range want {
			if strings.Count(got, text) != 1 {
				t.Fatalf("missing or repeated %q:\n%s", text, got)
			}
		}
	}
	parent, skipped := strings.Index(after, "✓ Upgrading space"), strings.Index(after, "Skipped child")
	if parent < 0 || strings.Index(after, "? Checking receipt") > parent || skipped < parent {
		t.Fatalf("unexpected order of urgent and grouped lines:\n%s", after)
	}
}

func TestCompletedChildrenLeaveRoomForRunningRows(t *testing.T) {
	l, _, scr, _ := setup(t, func() (int, int) { return 80, 5 })
	root := l.StartStep("Live parent")
	running := root.Step("Running child")
	_, _ = io.WriteString(running.Detail(), "live detail\n")
	for i := range 5 {
		root.Step(fmt.Sprintf("Finished child %d", i)).Done()
	}
	if got := scr.text(); !strings.Contains(got, "Live parent") || !strings.Contains(got, "Running child") || !strings.Contains(got, "live detail") {
		t.Fatalf("completions crowded out running progress:\n%s", got)
	}
}

func TestLateParentCompletionPrecedesItsChildAfterRootFailure(t *testing.T) {
	l, _, scr, _ := setup(t, nil)
	root := l.StartStep("Root failed")
	parent := root.Step("Late parent")
	parent.Step("Nested completion").Done()
	root.Fail(errors.New("parent still running"))
	if got := scr.text(); strings.Contains(got, "Nested completion") {
		t.Fatalf("child escaped the unfinished parent:\n%s", got)
	}
	parent.Done()
	if got := scr.text(); !strings.Contains(got, "\n  ✓ Late parent") || !strings.Contains(got, "\n    ✓ Nested completion") || strings.Index(got, "Late parent") > strings.Index(got, "Nested completion") {
		t.Fatalf("late completions missing or out of order:\n%s", got)
	}
}

func TestCloseRetainsCompletionsBelowUnfinishedIntermediateSteps(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Root")
	parent := root.Step("Unfinished outer step").Step("Finished parent")
	parent.Step("Unfinished inner step").Step("Finished descendant").Done()
	parent.Done()
	root.Done()
	v.Close()
	got := scr.text()
	if strings.Count(got, "Finished parent") != 1 || strings.Count(got, "Finished descendant") != 1 || strings.Contains(got, "Unfinished") {
		t.Fatalf("lost or duplicated a completion across unfinished steps:\n%s", got)
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
