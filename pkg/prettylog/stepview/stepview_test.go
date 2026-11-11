package stepview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// screen is a minimal terminal: printable text, CR, LF, CUU (ESC[nA), ED
// (ESC[J) and ignored SGR. It is enough to check what a View leaves visible.
type screen struct {
	mu   sync.Mutex
	rows [][]rune
	row  int
	col  int
	raw  strings.Builder
}

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw.Write(p)
	text := []rune(string(p))
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\r':
			s.col = 0
		case c == '\n':
			s.row++
		case c == 0x1b && i+1 < len(text) && text[i+1] == '[':
			j := i + 2
			for j < len(text) && (text[j] >= '0' && text[j] <= '9' || text[j] == ';') {
				j++
			}
			if j >= len(text) {
				return len(p), nil
			}
			n, _ := strconv.Atoi(string(text[i+2 : j]))
			switch text[j] {
			case 'A':
				s.row = max(s.row-max(n, 1), 0)
			case 'J':
				s.grow()
				s.rows[s.row] = s.rows[s.row][:min(s.col, len(s.rows[s.row]))]
				s.rows = s.rows[:s.row+1]
			}
			i = j
		default:
			s.grow()
			line := s.rows[s.row]
			for len(line) < s.col {
				line = append(line, ' ')
			}
			if s.col < len(line) {
				line[s.col] = c
			} else {
				line = append(line, c)
			}
			s.rows[s.row] = line
			s.col++
		}
	}
	return len(p), nil
}

func (s *screen) grow() {
	for len(s.rows) <= s.row {
		s.rows = append(s.rows, nil)
	}
}

func (s *screen) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := make([]string, len(s.rows))
	for i, r := range s.rows {
		lines[i] = strings.TrimRight(string(r), " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T, size func() (int, int)) (*prettylog.Logger, *View, *screen, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	scr := &screen{}
	l := prettylog.New(scr, prettylog.Options{Format: prettylog.FormatPretty, Verbosity: prettylog.Normal, ActorWidth: 20, Now: c.now})
	v := Install(l, scr, Options{ActorWidth: 20, Size: size, Interval: time.Hour, Now: c.now})
	t.Cleanup(v.Close)
	return l, v, scr, c
}

func TestBlockShowsRunningStepsAndCollapsesOnSuccess(t *testing.T) {
	l, v, scr, c := setup(t, nil)
	root := l.StartStep("Building image a1b2c3", prettylog.Actor("builder"), prettylog.Executor("buildkit"))
	base := root.Step("[1/3] FROM ubuntu")
	c.add(1200 * time.Millisecond)
	base.Done(slogBool("cached"))
	run := root.Step("[2/3] RUN npm install")
	_, _ = io.WriteString(run.Detail(), "added 3 packages\n")
	c.add(3 * time.Second)
	v.mu.Lock()
	v.frame = 3
	v.updateLocked(false)
	v.mu.Unlock()
	got := scr.text()
	for _, want := range []string{"⠸ builder · buildkit", "Building image a1b2c3", "✓ [1/3] FROM ubuntu", "⠸ [2/3] RUN npm install", "added 3 packages", "4s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in block:\n%s", want, got)
		}
	}
	run.Done()
	c.add(time.Second)
	root.Done()
	got = scr.text()
	want := "✓ builder · buildkit    Building image a1b2c3  5.2s"
	if got != want {
		t.Fatalf("scrollback:\n%q\nwant:\n%q", got, want)
	}
	if v.shown != 0 || v.ticker != nil {
		t.Fatal("block or ticker left after the last root step ended")
	}
}

func TestFailedStepExpandsItsOutput(t *testing.T) {
	l, _, scr, c := setup(t, nil)
	root := l.StartStep("Building image a1b2c3", prettylog.Actor("builder"))
	ok := root.Step("[1/3] FROM ubuntu")
	ok.Done()
	bad := root.Step("[2/3] RUN make")
	for i := range 25 {
		_, _ = io.WriteString(bad.Detail(), "line "+strconv.Itoa(i)+"\n")
	}
	c.add(2 * time.Second)
	bad.Fail(errors.New("exit code: 2"))
	root.Fail(errors.New("image preparation failed (job a1b2c3): exit code: 2"))
	got := scr.text()
	for _, want := range []string{"✗ builder", "Building image a1b2c3", "image preparation failed", "✗ [2/3] RUN make  2s", "exit code: 2", "line 24", "line 5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "line 4\n") || strings.Contains(got, "FROM ubuntu") {
		t.Fatalf("expanded more than the failed step's last %d lines:\n%s", failedTail, got)
	}
}

func TestOtherLinesPrintAboveTheBlock(t *testing.T) {
	l, _, scr, _ := setup(t, nil)
	root := l.StartStep("Building image a1b2c3", prettylog.Actor("builder"))
	l.Slog().Warn("BuildKit cache is almost full")
	got := scr.text()
	lines := strings.Split(got, "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "! BuildKit cache is almost full") || !strings.Contains(lines[1], "Building image") {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(scr.raw.String(), "full\r\n") {
		t.Fatal("Logger lines are not CRLF on the terminal")
	}
	root.Done()
}

func TestSuspendHidesAndResumeRedraws(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Building image a1b2c3", prettylog.Actor("builder"))
	v.Suspend()
	if got := scr.text(); got != "" {
		t.Fatalf("block visible while suspended: %q", got)
	}
	child := root.Step("[1/1] RUN true")
	if got := scr.text(); got != "" {
		t.Fatalf("redrawn while suspended: %q", got)
	}
	v.Resume()
	if got := scr.text(); !strings.Contains(got, "Building image") || !strings.Contains(got, "RUN true") {
		t.Fatalf("not redrawn after resume: %q", got)
	}
	child.Done()
	root.Done()
}

func TestStreamWriterKeepsBytesAndWaitsForLineEnd(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Building image a1b2c3", prettylog.Actor("builder"))
	w := v.Writer(scr)
	_, _ = io.WriteString(w, "partial")
	if got := scr.text(); got != "partial" {
		t.Fatalf("block drawn after a partial line: %q", got)
	}
	_, _ = io.WriteString(w, " output\n")
	if got := scr.text(); !strings.HasPrefix(got, "partial output\n") || !strings.Contains(got, "Building image") {
		t.Fatalf("%q", got)
	}
	root.Done()
}

func TestRowsAreCutToTheTerminal(t *testing.T) {
	l, _, scr, _ := setup(t, func() (int, int) { return 30, 5 })
	root := l.StartStep("Building image with a rather long description", prettylog.Actor("builder"))
	for i := range 10 {
		root.Step("step " + strconv.Itoa(i)).Done()
	}
	got := scr.text()
	rows := strings.Split(got, "\n")
	if len(rows) > 3 {
		t.Fatalf("block taller than height-2: %q", got)
	}
	for _, r := range rows {
		if n := len([]rune(r)); n > 29 {
			t.Fatalf("row wider than width-1 (%d): %q", n, r)
		}
	}
	root.Done()
}

// A step still running when the command ends (Ctrl-C, a lost connection)
// is left as uncertain, never as running or done, and nothing is redrawn.
func TestCloseMarksUnfinishedStepsAndRestoresTheLogger(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Building image a1b2c3", prettylog.Actor("builder"))
	root.Step("[1/2] FROM ubuntu").Done()
	root.Step("[2/2] RUN sleep 120")
	v.Close()
	if Current() != nil {
		t.Fatal("still current after Close")
	}
	got := scr.text()
	if !strings.Contains(got, "? builder") || !strings.Contains(got, "did not finish before the command ended") || strings.ContainsAny(got, "›✓⠋") {
		t.Fatalf("%q", got)
	}
	l.Slog().Info("after close")
	if got := scr.text(); !strings.HasSuffix(got, "after close") {
		t.Fatalf("Logger not restored: %q", got)
	}
}

func slogBool(key string) slog.Attr { return slog.Bool(key, true) }

// A late follower's snapshot of a failed build shows the failed sub-step's
// output, once, after the replay ends.
func TestLateSnapshotKeepsFailureDetail(t *testing.T) {
	l := prettylog.New(io.Discard, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Trace})
	tracker := prettylog.NewTracker(prettylog.TrackerLimits{})
	restore := l.Route(io.Discard, tracker)
	root := l.StartStep("Building image a1b2c3", prettylog.Actor("builder"))
	child := root.Step("[2/3] RUN make")
	_, _ = io.WriteString(child.Detail(), "CRITICAL COMPILER DETAIL\n")
	child.Fail(errors.New("exit code: 2"))
	root.Fail(errors.New("image preparation failed"))
	restore()
	scr := &screen{}
	v := New(scr, Options{Interval: time.Hour})
	defer v.Close()
	for _, r := range tracker.Snapshot() {
		v.Apply(r)
		if got := scr.text(); got != "" {
			t.Fatalf("summarized before the replay ended: %q", got)
		}
	}
	end := slog.NewRecord(time.Now(), prettylog.LevelDebug, "build steps replayed", 0)
	end.AddAttrs(slog.Bool(prettylog.KeySnapshot, true))
	v.Apply(end)
	got := scr.text()
	if strings.Count(got, "Building image a1b2c3") != 1 || !strings.Contains(got, "CRITICAL COMPILER DETAIL") || !strings.Contains(got, "✗ [2/3] RUN make") {
		t.Fatalf("%q", got)
	}
	v.Apply(end)
	if scr.text() != got {
		t.Fatal("summary repeated")
	}
}

// While suspended the View writes nothing, not even a finished step's
// summary; Resume writes it.
func TestSuspendDefersCompletionOutput(t *testing.T) {
	l, v, scr, _ := setup(t, nil)
	root := l.StartStep("Building image a1b2c3", prettylog.Actor("builder"))
	v.Suspend()
	before := scr.raw.String()
	root.Fail(errors.New("exit code: 2"))
	if scr.raw.String() != before {
		t.Fatalf("wrote while suspended: %q", strings.TrimPrefix(scr.raw.String(), before))
	}
	v.Resume()
	if got := scr.text(); !strings.Contains(got, "✗ builder") || !strings.Contains(got, "exit code: 2") {
		t.Fatalf("%q", got)
	}
}

// Without a steps block, a failed step's last output still reaches the
// user at quiet levels, with the plain format's "error:" prefix.
func TestFailuresExpandAFailedStepInLineOutput(t *testing.T) {
	var out strings.Builder
	l := prettylog.New(&out, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Quiet})
	f := NewFailures(l)
	var src strings.Builder
	w := prettylog.New(&src, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Trace})
	tr := recordTee{f}
	restore := w.Route(io.Discard, tr)
	root := w.StartStep("Building image a1b2c3", prettylog.Actor("builder"))
	ok := root.Step("[1/2] RUN true")
	_, _ = io.WriteString(ok.Detail(), "fine\n")
	ok.Done()
	bad := root.Step("[2/2] RUN make")
	_, _ = io.WriteString(bad.Detail(), "before failure\nQA-MARKER-42\n")
	bad.Fail(errors.New("exit code: 3"))
	root.Fail(errors.New("image preparation failed"))
	restore()
	got := out.String()
	if !strings.Contains(got, "error: Last output of [2/2] RUN make:\n  before failure\n  QA-MARKER-42") || strings.Contains(got, "fine") || strings.ContainsAny(got, "✗✓›") {
		t.Fatalf("%q", got)
	}
	// A late follower's snapshot of the same failure expands it once, after
	// the replayed tail.
	out.Reset()
	f2 := NewFailures(l)
	for _, r := range f.tracker.Snapshot() {
		_ = f2.Handle(context.Background(), r)
	}
	end := slog.NewRecord(time.Now(), prettylog.LevelDebug, "build steps replayed", 0)
	end.AddAttrs(slog.Bool(prettylog.KeySnapshot, true))
	_ = f2.Handle(context.Background(), end)
	if got := out.String(); strings.Count(got, "Last output of [2/2] RUN make") != 1 || !strings.Contains(got, "QA-MARKER-42") {
		t.Fatalf("%q", got)
	}
}

// recordTee feeds routed step records to a Failures, like a host would.
type recordTee struct{ f *Failures }

func (t recordTee) Apply(r slog.Record) bool { _ = t.f.Handle(context.Background(), r); return true }

// A tail longer than one message keeps every line, the final one (usually
// the error) included, live and when replayed from a snapshot.
func TestFailuresKeepTheLastLineOfALongTail(t *testing.T) {
	var out strings.Builder
	l := prettylog.New(&out, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Quiet})
	f := NewFailures(l)
	src := prettylog.New(io.Discard, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Trace})
	restore := src.Route(io.Discard, recordTee{f})
	step := src.StartStep("RUN make")
	for i := range 20 {
		_, _ = io.WriteString(step.Detail(), fmt.Sprintf("line %02d %s\n", i, strings.Repeat("x", 90)))
	}
	_, _ = io.WriteString(step.Detail(), "FINAL-MARKER exit code 2\n")
	step.Fail(errors.New("exit code: 2"))
	restore()
	check := func(got string) {
		t.Helper()
		if !strings.Contains(got, "FINAL-MARKER exit code 2") || strings.Contains(got, "…(+") || !strings.Contains(got, "Last output of RUN make (continued):") {
			t.Fatalf("%q", got)
		}
		for i := 1; i < 20; i++ { // the Tracker keeps the last 20 lines
			if !strings.Contains(got, fmt.Sprintf("line %02d ", i)) {
				t.Fatalf("line %d missing: %q", i, got)
			}
		}
	}
	check(out.String())
	out.Reset()
	f2 := NewFailures(l)
	for _, r := range f.tracker.Snapshot() {
		_ = f2.Handle(context.Background(), r)
	}
	end := slog.NewRecord(time.Now(), prettylog.LevelDebug, "build steps replayed", 0)
	end.AddAttrs(slog.Bool(prettylog.KeySnapshot, true))
	_ = f2.Handle(context.Background(), end)
	check(out.String())
}

type expansionFails struct{ err error }

func (e expansionFails) Handle(_ context.Context, r slog.Record) error {
	if strings.HasPrefix(r.Message, "Last output of ") {
		return e.err
	}
	return nil
}

// A sink that accepts the failed step but not its expansion is reported.
func TestFailuresReturnTheExpansionError(t *testing.T) {
	boom := errors.New("terminal gone")
	f := NewFailures(expansionFails{boom})
	src := prettylog.New(io.Discard, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Trace})
	var last error
	restore := src.Route(io.Discard, applyFunc(func(r slog.Record) { last = f.Handle(context.Background(), r) }))
	step := src.StartStep("RUN make")
	_, _ = io.WriteString(step.Detail(), "boom\n")
	step.Fail(errors.New("exit code: 2"))
	restore()
	if !errors.Is(last, boom) {
		t.Fatalf("expansion error lost: %v", last)
	}
	f2 := NewFailures(expansionFails{boom})
	for _, r := range f.tracker.Snapshot() {
		_ = f2.Handle(context.Background(), r)
	}
	end := slog.NewRecord(time.Now(), prettylog.LevelDebug, "build steps replayed", 0)
	end.AddAttrs(slog.Bool(prettylog.KeySnapshot, true))
	if err := f2.Handle(context.Background(), end); !errors.Is(err, boom) {
		t.Fatalf("snapshot expansion error lost: %v", err)
	}
}

type applyFunc func(slog.Record)

func (a applyFunc) Apply(r slog.Record) bool { a(r); return true }

// A very long step title does not take the output's room: the heading cuts
// it, and the final line still shows, live and from a snapshot.
func TestFailuresKeepTheTailUnderALongTitle(t *testing.T) {
	var out strings.Builder
	l := prettylog.New(&out, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Quiet})
	f := NewFailures(l)
	src := prettylog.New(io.Discard, prettylog.Options{Format: prettylog.FormatPlain, Verbosity: prettylog.Trace})
	restore := src.Route(io.Discard, recordTee{f})
	step := src.StartStep("RUN " + strings.Repeat("é", 600))
	_, _ = io.WriteString(step.Detail(), "FINAL-MARKER\n")
	step.Fail(errors.New("exit code: 2"))
	restore()
	check := func(got string) {
		t.Helper()
		if !strings.Contains(got, "FINAL-MARKER") || !strings.Contains(got, "…:") {
			t.Fatalf("%q", got)
		}
	}
	check(out.String())
	out.Reset()
	f2 := NewFailures(l)
	for _, r := range f.tracker.Snapshot() {
		_ = f2.Handle(context.Background(), r)
	}
	end := slog.NewRecord(time.Now(), prettylog.LevelDebug, "build steps replayed", 0)
	end.AddAttrs(slog.Bool(prettylog.KeySnapshot, true))
	_ = f2.Handle(context.Background(), end)
	check(out.String())
}

// A build root that ends retitled collapses to that one line.
func TestRetitledRootCollapsesToOneLine(t *testing.T) {
	l, _, scr, _ := setup(t, nil)
	root := slog.NewRecord(time.Now(), prettylog.LevelInfo, "Image for box ok1 (features: f1)", 0)
	root.AddAttrs(prettylog.Actor("builder"), slog.String(prettylog.KeyStep, "r"), slog.String(prettylog.KeyState, "running"))
	_ = l.Handle(context.Background(), root)
	build := slog.NewRecord(time.Now(), prettylog.LevelInfo, "Building image local/c:1", 0)
	build.AddAttrs(slog.String(prettylog.KeyStep, "b"), slog.String(prettylog.KeyParent, "r"), slog.String(prettylog.KeyState, "running"))
	_ = l.Handle(context.Background(), build)
	done := slog.NewRecord(time.Now(), prettylog.LevelInfo, "Building image local/c:1", 0)
	done.AddAttrs(slog.String(prettylog.KeyStep, "b"), slog.String(prettylog.KeyParent, "r"), slog.String(prettylog.KeyState, "ok"), prettylog.Took(time.Second))
	_ = l.Handle(context.Background(), done)
	ready := slog.NewRecord(time.Now(), prettylog.LevelInfo, "Image for box ok1 ready (features: f1)", 0)
	ready.AddAttrs(prettylog.Actor("builder"), slog.String(prettylog.KeyStep, "r"), slog.String(prettylog.KeyState, "ok"), prettylog.Took(71*time.Second))
	_ = l.Handle(context.Background(), ready)
	if got := scr.text(); got != "✓ builder               Image for box ok1 ready (features: f1)  1m11s" {
		t.Fatalf("%q", got)
	}
}
