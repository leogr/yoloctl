package prettylog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type emissionFunc func(Emission) error

func (f emissionFunc) Emit(e Emission) error { return f(e) }

func TestStructuredEmissionKeepsFirstOwnerAndOrdering(t *testing.T) {
	var bytesWritten bytes.Buffer
	var got []Emission
	l := New(&bytesWritten, Options{Format: FormatPretty, Verbosity: Normal,
		Fold: FoldOptions{Window: time.Hour}, Emit: emissionFunc(func(e Emission) error { got = append(got, e); return nil })})
	ctx := context.Background()
	a := recAt(t0, LevelNotice, "A")
	_ = l.HandleOwned(ctx, a, 101)
	a.Time = t0.Add(time.Second)
	_ = l.HandleOwned(ctx, a, 102)
	_ = l.HandleOwned(ctx, recAt(t0.Add(2*time.Second), LevelDebug, "filtered"), 999)
	a.Time = t0.Add(3 * time.Second)
	_ = l.HandleOwned(ctx, a, 103)
	_ = l.HandleOwned(ctx, recAt(t0.Add(4*time.Second), LevelNotice, "B"), 201)
	_ = l.HandleOwned(ctx, recAt(t0.Add(5*time.Second), LevelNotice, "A"), 301)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Kind != EmissionRecord || got[0].Owner != 101 || got[0].Record.Message != "A" ||
		got[1].Kind != EmissionRepeatSummary || got[1].Owner != 101 || got[1].Repeat != 2 || !got[1].Until.Equal(t0.Add(3*time.Second)) ||
		got[2].Owner != 201 || got[2].Record.Message != "B" || got[3].Owner != 301 || got[3].Record.Message != "A" {
		t.Fatalf("emissions: %+v", got)
	}
	if got[1].Record.Message != "" || bytesWritten.Len() != 0 {
		t.Fatal("summary fabricated a record or sink wrote bytes")
	}
	for _, e := range got {
		if strings.HasSuffix(e.Text, "\n") {
			t.Fatal("Text acquired a final newline")
		}
	}
	// Close drains once; later records retain the pre-existing unfolded behavior.
	_ = l.Close()
	_ = l.Flush()
	_ = l.HandleOwned(ctx, a, 401)
	_ = l.HandleOwned(ctx, a, 402)
	if len(got) != 6 || got[4].Owner != 401 || got[5].Owner != 402 {
		t.Fatalf("post-Close: %+v", got)
	}
}

func TestStructuredEmissionTimerFlushCloseAndStickyError(t *testing.T) {
	ch := make(chan Emission, 8)
	failure := errors.New("sink unavailable")
	l := New(io.Discard, Options{Format: FormatPlain, Verbosity: Normal, Fold: FoldOptions{Window: 20 * time.Millisecond}, Emit: emissionFunc(func(e Emission) error {
		ch <- e
		if e.Kind == EmissionRepeatSummary {
			return failure
		}
		return nil
	})})
	r := recAt(t0, LevelWarn, "same")
	_ = l.HandleOwned(t.Context(), r, 1)
	_ = l.HandleOwned(t.Context(), r, 2)
	for _, kind := range []EmissionKind{EmissionRecord, EmissionRepeatSummary} {
		select {
		case e := <-ch:
			if e.Kind != kind || e.Owner != 1 {
				t.Fatalf("timer: %+v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timer did not emit")
		}
	}
	if !errors.Is(l.Flush(), failure) || !errors.Is(l.Close(), failure) || !errors.Is(l.Err(), failure) {
		t.Fatal("sink error was not sticky")
	}
	select {
	case e := <-ch:
		t.Fatalf("duplicate summary: %+v", e)
	default:
	}
}

func TestStructuredEmissionLeavesDefaultBytesWireAndStepsUnchanged(t *testing.T) {
	for _, format := range []Format{FormatPretty, FormatPlain, FormatWire} {
		var owned, plain bytes.Buffer
		a := New(&owned, Options{Format: format, Verbosity: Trace})
		b := New(&plain, Options{Format: format, Verbosity: Trace})
		r := recAt(t0, LevelWarn, "same", slog.String("path", "/tmp/file"))
		_ = a.HandleOwned(t.Context(), r, 777)
		_ = b.Handle(t.Context(), r)
		_ = a.Close()
		_ = b.Close()
		if !bytes.Equal(owned.Bytes(), plain.Bytes()) {
			t.Fatalf("owner affected format %v", format)
		}
	}
	var wire bytes.Buffer
	called := false
	l := New(&wire, Options{Format: FormatWire, Verbosity: Trace, Emit: emissionFunc(func(Emission) error { called = true; return nil })})
	_ = l.HandleOwned(t.Context(), recAt(t0, LevelWarn, "wire"), 777)
	if called {
		t.Fatal("wire went through presentation sink")
	}
	r, err := NewWireReader(&wire).Next()
	if err != nil || r.Message != "wire" {
		t.Fatal("wire changed", err)
	}
	tr := NewTracker(TrackerLimits{})
	var lines bytes.Buffer
	steps := New(io.Discard, Options{Format: FormatPlain, Verbosity: Trace, Emit: emissionFunc(func(Emission) error { called = true; return nil })})
	restore := steps.Route(&lines, tr)
	root := steps.StartStep("step")
	root.Done()
	restore()
	if called || len(tr.Steps()) != 1 || tr.Steps()[0].State != StepOK {
		t.Fatal("Route/steps interception changed")
	}
}

func TestStructuredEmissionRecordFailureIsReturnedWithoutRetry(t *testing.T) {
	failure := errors.New("record sink failed")
	calls := 0
	l := New(io.Discard, Options{Verbosity: Normal, Emit: emissionFunc(func(Emission) error { calls++; return failure })})
	if err := l.HandleOwned(t.Context(), recAt(t0, LevelInfo, "shown once"), 1); !errors.Is(err, failure) {
		t.Fatal("lost record error", err)
	}
	if err := l.Close(); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("error retried or lost on close: calls=%d err=%v", calls, err)
	}
}

func TestStructuredEmissionSerializesConcurrentCallers(t *testing.T) {
	var owners []uint64
	l := New(io.Discard, Options{Verbosity: Normal, Emit: emissionFunc(func(e Emission) error { owners = append(owners, e.Owner); return nil })})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() { _ = l.HandleOwned(t.Context(), recAt(t0, LevelInfo, "record"), uint64(i+1)) })
	}
	wg.Wait()
	_ = l.Close()
	if len(owners) != 32 {
		t.Fatalf("lost records: %v", owners)
	}
	seen := map[uint64]bool{}
	for _, owner := range owners {
		if seen[owner] {
			t.Fatal("owner changed")
		}
		seen[owner] = true
	}
}
