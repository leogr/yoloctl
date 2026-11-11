package prettylog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func recAt(t time.Time, level slog.Level, msg string, args ...any) slog.Record {
	r := slog.NewRecord(t, level, msg, 0)
	r.Add(args...)
	return r
}

func TestFoldConsecutiveIdenticalRecords(t *testing.T) {
	l, b := newTest(Options{Format: FormatPretty, Verbosity: Normal, Fold: FoldOptions{Window: 10 * time.Second}})
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_ = l.Handle(ctx, recAt(t0.Add(time.Duration(i)*time.Second), LevelWarn, "disk slow", Actor("vm")))
	}
	_ = l.Handle(ctx, recAt(t0.Add(5*time.Second), LevelWarn, "other"))
	_ = l.Handle(ctx, recAt(t0.Add(6*time.Second), LevelWarn, "disk slow", Actor("vm")))
	// A gap longer than the window starts a new run.
	_ = l.Handle(ctx, recAt(t0.Add(30*time.Second), LevelWarn, "disk slow", Actor("vm")))
	_ = l.Handle(ctx, recAt(t0.Add(31*time.Second), LevelWarn, "disk slow", Actor("vm")))
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	until := func(d time.Duration) string { return t0.Add(d).Local().Format("15:04:05") }
	want := strings.Join([]string{
		"! vm  disk slow",
		"      ↳ repeated 3 more times until " + until(3*time.Second),
		"! other",
		"! vm  disk slow",
		"! vm  disk slow",
		"      ↳ repeated 1 more time until " + until(31*time.Second),
		""}, "\n")
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestFoldTimerEndsRun(t *testing.T) {
	var mu sync.Mutex
	var b bytes.Buffer
	w := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return b.Write(p) })
	l := New(w, Options{Format: FormatPlain, Verbosity: Normal, Fold: FoldOptions{Window: 20 * time.Millisecond}})
	now := time.Now()
	_ = l.Handle(context.Background(), recAt(now, LevelWarn, "x"))
	_ = l.Handle(context.Background(), recAt(now, LevelWarn, "x"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := b.String()
		mu.Unlock()
		if strings.Contains(got, "↳ repeated 1 more time") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("summary never written: %q", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = l.Close()
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestStepsLineOutput(t *testing.T) {
	for _, tc := range []struct {
		format Format
		want   string
	}{
		{FormatPretty, strings.Join([]string{
			"› builder · buildkit  Preparing image claude",
			"  › Base image",
			"  ✓ Base image  0s",
			"  › Feature claude",
			"    added 3 packages",
			"  ✗ Feature claude  0s",
			"    exit status 1",
			"  › Cleanup",
			"  ○ Cleanup  not needed",
			"? builder · buildkit  Preparing image claude",
			"                      connection lost",
			""}, "\n")},
		{FormatPlain, strings.Join([]string{
			"builder/buildkit: Preparing image claude",
			"  Base image",
			"  Base image: done (0s)",
			"  Feature claude",
			"    added 3 packages",
			"  error: Feature claude: failed (0s): exit status 1",
			"  Cleanup",
			"  Cleanup: skipped: not needed",
			"uncertain: builder/buildkit: Preparing image claude: connection lost",
			""}, "\n")},
	} {
		l, b := newTest(Options{Format: tc.format, Verbosity: Trace})
		root := l.StartStep("Preparing image claude", Actor("builder"), Executor("buildkit"))
		base := root.Step("Base image")
		base.Done()
		base.Done() // no-op
		feat := root.Step("Feature claude")
		_, _ = fmt.Fprint(feat.Detail(), "added 3 ")
		_, _ = fmt.Fprint(feat.Detail(), "packages\n")
		feat.Fail(errors.New("exit status 1"))
		root.Step("Cleanup").Skip("not needed")
		root.Unknown(errors.New("connection lost"))
		if b.String() != tc.want {
			t.Errorf("format %v:\n%s\nwant:\n%s", tc.format, b.String(), tc.want)
		}
	}
}

func buildTracker(t *testing.T, lim TrackerLimits) (*Tracker, *Logger) {
	t.Helper()
	tr := NewTracker(lim)
	var wire bytes.Buffer
	l := New(&wire, Options{Format: FormatWire, Verbosity: Quiet})
	root := l.StartStep("build", Actor("builder"), slog.String("image", "claude"))
	ok := root.Step("ok step")
	_, _ = fmt.Fprintln(ok.Detail(), "ok line")
	ok.Done(slog.Bool("cached", true))
	failed := root.Step("failed step")
	for i := 0; i < 30; i++ {
		_, _ = fmt.Fprintf(failed.Detail(), "line %02d\n", i)
	}
	failed.Fail(errors.New("boom"))
	root.Step("skipped").Skip("nothing to do")
	unk := root.Step("unknown")
	unk.Unknown(errors.New("lost"))
	run := root.Step("still running")
	_, _ = fmt.Fprintln(run.Detail(), "partial work")
	rd := NewWireReader(&wire)
	for {
		r, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		tr.Apply(r)
	}
	return tr, l
}

func TestTrackerSnapshotRoundTrip(t *testing.T) {
	lim := TrackerLimits{Steps: 5, StepDetail: 200, TotalDetail: 250}
	tr, _ := buildTracker(t, lim)
	steps := tr.Steps()
	if len(steps) != 5 || tr.Overflow() != 1 {
		t.Fatalf("steps %d overflow %d", len(steps), tr.Overflow())
	}
	if steps[2].State != StepFailed || steps[2].Err != "boom" || len(steps[2].Tail) == 0 || steps[2].Omitted == 0 {
		t.Fatalf("failed step %+v", steps[2])
	}
	// Also through the wire, at Quiet: snapshots must pass the worker filter.
	var wire bytes.Buffer
	l := New(&wire, Options{Format: FormatWire, Verbosity: Quiet})
	for _, r := range tr.Snapshot() {
		if err := l.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	again := NewTracker(lim)
	rd := NewWireReader(&wire)
	for {
		r, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		again.Apply(r)
	}
	got := again.Steps()
	for i := range steps {
		steps[i].Started, got[i].Started = steps[i].Started.UTC().Round(0), got[i].Started.UTC().Round(0)
	}
	if !reflect.DeepEqual(got, steps) || again.Overflow() != tr.Overflow() {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", got, steps)
	}
}

func TestTrackerStatesOnlyMoveForward(t *testing.T) {
	tr := NewTracker(TrackerLimits{})
	tr.Apply(recAt(t0, LevelInfo, "s", KeyStep, "a.1", KeyState, "running"))
	tr.Apply(recAt(t0.Add(time.Second), LevelInfo, "s", KeyStep, "a.1", KeyState, "ok", KeyTook, time.Second))
	tr.Apply(recAt(t0.Add(2*time.Second), LevelInfo, "s", KeyStep, "a.1", KeyState, "running"))
	tr.Apply(recAt(t0.Add(2*time.Second), LevelError, "s", KeyStep, "a.1", KeyState, "failed"))
	if s := tr.Steps(); len(s) != 1 || s[0].State != StepOK || s[0].Took != time.Second {
		t.Fatalf("%+v", s)
	}
}

func TestTrackerEvictsRunningWhenAllRun(t *testing.T) {
	tr := NewTracker(TrackerLimits{StepDetail: 100, TotalDetail: 10})
	tr.Apply(recAt(t0, LevelInfo, "a", KeyStep, "x.1", KeyState, "running"))
	tr.Apply(recAt(t0, LevelInfo, "b", KeyStep, "x.2", KeyState, "running"))
	tr.Apply(recAt(t0, LevelDebug, "12345678", KeyStep, "x.1", KeyDetail, true))
	tr.Apply(recAt(t0, LevelDebug, "abcd", KeyStep, "x.2", KeyDetail, true))
	s := tr.Steps()
	if len(s[0].Tail) != 0 || s[0].Omitted != 9 || len(s[1].Tail) != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestWireRoundTripKeepsTypes(t *testing.T) {
	r := recAt(t0, LevelNotice, "hello \x1b", "s", "x", "i", -3, "u", uint64(1<<63), "f", 1.5, "b", true,
		"d", 2*time.Second, "t", t0, slog.Group("g", "k", "v"), "e", errors.New("bad"))
	b, err := MarshalRecord(r, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalRecord(b)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	got.Attrs(func(a slog.Attr) bool { kinds = append(kinds, a.Key+":"+a.Value.Kind().String()); return true })
	want := []string{"s:String", "i:Int64", "u:Uint64", "f:Float64", "b:Bool", "d:Duration", "t:Time", "g.k:String", "e:Any"}
	if !reflect.DeepEqual(kinds, want) || got.Message != "hello \x1b" || !got.Time.Equal(t0) || got.Level != LevelNotice {
		t.Fatalf("got %v %q", kinds, got.Message)
	}
	if _, err := UnmarshalRecord([]byte(`{"t":"2026-11-11T00:00:00Z","l":0,"m":"","x":1}`)); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestWireCapsKeepStructure(t *testing.T) {
	big := strings.Repeat("\x1b", 3000) // escapes to 6 bytes each
	r := recAt(t0, LevelError, big, "opt1", big, "opt2", "v", KeyErr, big, KeyStep, "s.1", KeyState, "failed", KeyOutcome, "failed")
	b, err := MarshalRecord(r, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxWire {
		t.Fatalf("encoded %d bytes", len(b))
	}
	got, _ := UnmarshalRecord(b)
	st := StructureOf(got)
	if st.Step != "s.1" || st.State != "failed" || st.Outcome != "failed" || !st.Truncated {
		t.Fatalf("structure lost: %+v", st)
	}
	// Duplicate reserved keys: the last wins, once.
	d := recAt(t0, LevelInfo, "m", KeyState, "running", KeyStep, "a", KeyState, "ok")
	b, _ = MarshalRecord(d, Limits{})
	if strings.Count(string(b), `"state"`) != 1 || StructureOf(d).State != "ok" {
		t.Fatalf("duplicates: %s", b)
	}
}

func TestWireDropsUnrepresentableRecordExplicitly(t *testing.T) {
	var wire bytes.Buffer
	l := New(&wire, Options{Format: FormatWire, Verbosity: Normal})
	huge := strings.Repeat("x", 300)
	_ = l.Handle(context.Background(), recAt(t0, LevelInfo, "t", KeyStep, huge, KeyState, "running", KeySnapshot, true))
	if !errors.Is(l.Err(), ErrUnrepresentable) {
		t.Fatalf("err %v", l.Err())
	}
	r, err := NewWireReader(&wire).Next()
	if err != nil {
		t.Fatal(err)
	}
	st := StructureOf(r)
	if st.Step != "" || st.State != "" || st.Snapshot || r.Message != "log record dropped: too large to encode" {
		t.Fatalf("fallback carries structure: %+v %q", st, r.Message)
	}
	// With a tiny limit even the warning cannot be encoded: nothing is written.
	var tiny bytes.Buffer
	l = New(&tiny, Options{Format: FormatWire, Verbosity: Normal, Limits: Limits{Wire: 40}})
	_ = l.Handle(context.Background(), recAt(t0, LevelInfo, "t", KeyStep, huge, KeyState, "running"))
	if tiny.Len() != 0 || !errors.Is(l.Err(), ErrUnrepresentable) {
		t.Fatalf("wrote %q err %v", tiny.String(), l.Err())
	}
	// A large err in a snapshot fits after cutting and keeps its structure.
	snap := recAt(t0, LevelError, "s", KeySnapshot, true, KeyStep, "a.1", KeyState, "failed",
		KeyStarted, t0, KeyOmitted, int64(5), KeyErr, strings.Repeat("e", 20000))
	b, err := MarshalRecord(snap, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := UnmarshalRecord(b)
	if st := StructureOf(got); !st.Snapshot || st.Omitted != 5 || !st.Started.Equal(t0) || !strings.HasSuffix(st.Err, " bytes)") {
		t.Fatalf("snapshot after cut: %+v", st)
	}
}

func TestWireReaderIncomplete(t *testing.T) {
	var wire bytes.Buffer
	l := New(&wire, Options{Format: FormatWire, Verbosity: Normal})
	l.Slog().Info("one")
	full := wire.Bytes()
	if _, err := NewWireReader(bytes.NewReader(full[:len(full)-2])).Next(); !errors.Is(err, ErrIncompleteRecord) {
		t.Fatalf("err %v", err)
	}
	if _, err := NewWireReader(bytes.NewReader(full[:2])).Next(); !errors.Is(err, ErrIncompleteRecord) {
		t.Fatalf("err %v", err)
	}
}

func TestBlockingWriterLosesNothing(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var got []string
	w := writerFunc(func(p []byte) (int, error) {
		<-release
		mu.Lock()
		got = append(got, string(p))
		mu.Unlock()
		return len(p), nil
	})
	l := New(w, Options{Format: FormatPlain, Verbosity: Normal})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			l.Slog().Info(fmt.Sprint("r", i))
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("writes did not block")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	if strings.Join(got, "") != "r0\nr1\nr2\n" {
		t.Fatalf("%q", got)
	}
}

func TestDerivedHandlersNeverInterleave(t *testing.T) {
	var b bytes.Buffer
	l := New(&b, Options{Format: FormatPlain, Verbosity: Normal})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			s := l.Slog().With("g", g).WithGroup("x")
			for i := 0; i < 200; i++ {
				s.Info("line\nsecond", "i", i)
			}
		}(g)
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 8*200*2 {
		t.Fatalf("%d lines", len(lines))
	}
	for i := 0; i < len(lines); i += 2 {
		if !strings.HasPrefix(lines[i], "line") || lines[i+1] != "  second" {
			t.Fatalf("interleaved at %d: %q %q", i, lines[i], lines[i+1])
		}
	}
}

func TestReservedKeysInsideGroupsAreFields(t *testing.T) {
	l, b := newTest(Options{Format: FormatPlain, Verbosity: Verbose})
	l.Slog().WithGroup("g").Info("m", KeyState, "ok", KeyOutcome, "failed")
	if b.String() != "m g.state=ok g.outcome=failed\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestRouteSendsStepsToSinkAndLinesToWriter(t *testing.T) {
	l, orig := newTest(Options{Format: FormatPlain, Verbosity: Quiet})
	var routed bytes.Buffer
	tr := NewTracker(TrackerLimits{})
	restore := l.Route(&routed, tr)
	s := l.StartStep("build")
	_, _ = fmt.Fprintln(s.Detail(), "out")
	s.Done()
	l.Slog().Warn("careful")
	restore()
	l.Slog().Warn("after")
	if got := tr.Steps(); len(got) != 1 || got[0].State != StepOK || len(got[0].Tail) != 1 {
		t.Fatalf("tracker %+v", got)
	}
	if routed.String() != "warning: careful\n" || orig.String() != "warning: after\n" {
		t.Fatalf("routed %q original %q", routed.String(), orig.String())
	}
}
