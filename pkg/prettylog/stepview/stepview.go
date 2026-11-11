// Package stepview is the interactive inline steps block: while root steps
// run, it keeps a short live block at the bottom of a terminal (spinner,
// elapsed time, the running step's latest output) and prints every other
// line above it. A finished root step leaves one collapsed line in the
// scrollback; a failed one also leaves its failed sub-steps and their last
// output lines.
//
// A View never reads the terminal and never owns the keyboard. It is driven
// by the step records of one prettylog.Logger (Install routes them to it) and
// renders through the same line grammar, glyphs and palette as the Logger.
package stepview

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Options configures a View. Colour, palette and actor width should match
// the Logger the View is installed on.
type Options struct {
	Color      bool
	Palette    prettylog.Palette
	ActorWidth int
	// Verbosity Verbose shows the running steps' last 10 output lines
	// instead of 1.
	Verbosity prettylog.Verbosity
	// Size reports the terminal size; nil or a non-positive answer means
	// 80×24. The block never takes more than Height-2 rows and never
	// wraps: rows are cut to Width-1 cells.
	Size func() (width, height int)
	// Interval is the redraw cadence while steps run (0 = 100 ms).
	Interval time.Duration
	// Now is the clock (nil = time.Now).
	Now func() time.Time
}

const (
	maxChildren = 6
	failedTail  = prettylog.TailLines
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// View is an inline steps block on one terminal writer. Its methods are
// safe for concurrent use. Apply never calls back into a Logger.
type View struct {
	mu      sync.Mutex
	out     io.Writer
	o       Options
	tracker *prettylog.Tracker
	actors  map[string][2]string // root step id → actor, executor
	active  []string             // root step ids in the block, in start order
	shown   int                  // block rows currently on screen
	partial bool                 // passthrough output left the cursor mid-line
	pause   int                  // Suspend depth
	// replaying is set while a snapshot replay is in progress: its records
	// restore steps one by one, so ended roots are summarized only once the
	// replay ends (a step-less snapshot record or any live record).
	replaying bool
	closed    bool
	frame     int
	dirty     bool
	ticker    *time.Ticker
	stop      chan struct{}
	restore   func()
}

// New returns a View writing to out (a terminal).
func New(out io.Writer, o Options) *View {
	if o.Interval <= 0 {
		o.Interval = 100 * time.Millisecond
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &View{out: out, o: o, tracker: prettylog.NewTracker(prettylog.TrackerLimits{}), actors: map[string][2]string{}}
}

var current atomic.Pointer[View]

// Install routes l's step records to a new View on out and l's other lines
// above its block, and makes it Current. Close undoes both.
func Install(l *prettylog.Logger, out io.Writer, o Options) *View {
	v := New(out, o)
	v.restore = l.Route(v.Lines(), v)
	current.Store(v)
	return v
}

// Current is the installed View, or nil. Every View method accepts a nil
// receiver, so callers may write stepview.Current().Suspend().
func Current() *View { return current.Load() }

// Apply is the prettylog.StepSink: it reduces one step record and updates
// the block.
func (v *View) Apply(r slog.Record) bool {
	if v == nil {
		return false
	}
	st := prettylog.StructureOf(r)
	v.mu.Lock()
	defer v.mu.Unlock()
	applied := v.tracker.Apply(r)
	v.replaying = st.Snapshot && st.Step != ""
	if !applied && !st.Snapshot {
		return false
	}
	if st.Step != "" && st.State != "" {
		// Roots are recognised from the reconstructed step, not from the
		// record: snapshot detail lines carry no parent either.
		if _, seen := v.actors[st.Step]; !seen {
			for _, s := range v.tracker.Steps() {
				if s.ID == st.Step && s.Parent == "" {
					v.actors[st.Step] = [2]string{st.Actor, st.Executor}
					v.active = append(v.active, st.Step)
					break
				}
			}
		}
	}
	v.updateLocked(st.Detail && st.State == "")
	return applied
}

// updateLocked flushes ended root steps to the scrollback and redraws. A
// detail line alone only marks the block dirty (the ticker redraws it), so
// a burst of executor output costs at most one redraw per interval.
//
// While suspended or replaying a snapshot it writes nothing: ended roots stay
// pending and are summarized by Resume, the end of the replay or Close.
func (v *View) updateLocked(detailOnly bool) {
	if v.closed {
		return
	}
	if v.pause > 0 || v.replaying {
		v.dirty = true
		return
	}
	steps := v.tracker.Steps()
	byID := make(map[string]prettylog.StepView, len(steps))
	for _, s := range steps {
		byID[s.ID] = s
	}
	var ended []string
	kept := v.active[:0]
	for _, id := range v.active {
		if s, ok := byID[id]; ok && s.State != prettylog.StepRunning {
			ended = append(ended, id)
			continue
		}
		kept = append(kept, id)
	}
	v.active = kept
	if len(ended) == 0 && detailOnly {
		v.dirty = true
		return
	}
	var b bytes.Buffer
	v.clearLocked(&b)
	for _, id := range ended {
		b.WriteString(v.summaryLocked(byID[id], steps))
	}
	v.drawLocked(&b, steps)
	_, _ = v.out.Write(b.Bytes())
	v.tickLocked()
}

// tickLocked runs the redraw ticker only while the block is visible.
func (v *View) tickLocked() {
	running := len(v.active) > 0 && v.pause == 0 && !v.closed
	switch {
	case running && v.ticker == nil:
		v.ticker = time.NewTicker(v.o.Interval)
		v.stop = make(chan struct{})
		go v.loop(v.ticker, v.stop)
	case !running && v.ticker != nil:
		v.ticker.Stop()
		close(v.stop)
		v.ticker, v.stop = nil, nil
	}
}

func (v *View) loop(t *time.Ticker, stop chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			v.mu.Lock()
			if v.stop == stop {
				v.frame++
				v.dirty = false
				var b bytes.Buffer
				v.clearLocked(&b)
				v.drawLocked(&b, v.tracker.Steps())
				_, _ = v.out.Write(b.Bytes())
			}
			v.mu.Unlock()
		}
	}
}

func (v *View) size() (int, int) {
	w, h := 80, 24
	if v.o.Size != nil {
		if sw, sh := v.o.Size(); sw > 0 && sh > 0 {
			w, h = sw, sh
		}
	}
	return w, h
}

// clearLocked erases the block; the cursor ends at the block's first column.
func (v *View) clearLocked(b *bytes.Buffer) {
	if v.shown == 0 {
		return
	}
	b.WriteString("\r")
	if v.shown > 1 {
		b.WriteString("\x1b[" + strconv.Itoa(v.shown-1) + "A")
	}
	b.WriteString("\x1b[J")
	v.shown = 0
}

func (v *View) drawLocked(b *bytes.Buffer, steps []prettylog.StepView) {
	if v.pause > 0 || v.closed || v.partial || len(v.active) == 0 {
		return
	}
	width, height := v.size()
	rows := v.blockRows(steps)
	if limit := max(height-2, 1); len(rows) > limit {
		rows = rows[len(rows)-limit:]
	}
	for i, row := range rows {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(ansi.Truncate(row, max(width-1, 1), "…"))
	}
	v.shown = len(rows)
}

// renderer formats synthetic step records with the Logger's grammar.
type renderer struct {
	buf bytes.Buffer
	l   *prettylog.Logger
}

func (v *View) renderer(glyphs prettylog.Glyphs) *renderer {
	r := &renderer{}
	r.l = prettylog.New(&r.buf, prettylog.Options{
		Format: prettylog.FormatPretty, Color: v.o.Color, Palette: v.o.Palette, Glyphs: glyphs,
		Verbosity: prettylog.Trace, ActorWidth: v.o.ActorWidth, Now: v.o.Now,
	})
	return r
}

func (r *renderer) step(s prettylog.StepView, actor [2]string, took time.Duration, withErr bool) {
	level := prettylog.LevelInfo
	switch s.State {
	case prettylog.StepFailed:
		level = prettylog.LevelError
	case prettylog.StepUnknown:
		level = prettylog.LevelWarn
	}
	rec := slog.NewRecord(time.Time{}, level, s.Title, 0)
	rec.AddAttrs(slog.String(prettylog.KeyStep, s.ID), slog.String(prettylog.KeyState, string(s.State)))
	if s.Parent != "" {
		rec.AddAttrs(slog.String(prettylog.KeyParent, s.Parent))
	} else {
		if actor[0] != "" {
			rec.AddAttrs(prettylog.Actor(actor[0]))
		}
		if actor[1] != "" {
			rec.AddAttrs(prettylog.Executor(actor[1]))
		}
	}
	if took > 0 {
		rec.AddAttrs(prettylog.Took(took))
	}
	if withErr && s.Err != "" {
		rec.AddAttrs(slog.String(prettylog.KeyErr, s.Err))
	}
	if s.State == prettylog.StepSkipped && s.Reason != "" {
		rec.AddAttrs(slog.String(prettylog.KeyReason, s.Reason))
	}
	_ = r.l.Handle(context.Background(), rec)
}

func (r *renderer) detail(s prettylog.StepView, text string) {
	rec := slog.NewRecord(time.Time{}, prettylog.LevelDebug, text, 0)
	rec.AddAttrs(slog.String(prettylog.KeyStep, s.ID), slog.Bool(prettylog.KeyDetail, true))
	_ = r.l.Handle(context.Background(), rec)
}

func (r *renderer) lines() []string {
	text := strings.TrimSuffix(r.buf.String(), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func children(steps []prettylog.StepView, parent string) []prettylog.StepView {
	var out []prettylog.StepView
	for _, s := range steps {
		if s.Parent == parent {
			out = append(out, s)
		}
	}
	return out
}

// blockRows renders the live block: each active root, then its most recent
// sub-steps (older ones counted), each running one with its last output.
func (v *View) blockRows(steps []prettylog.StepView) []string {
	glyphs := prettylog.DefaultGlyphs
	glyphs.Info = spinner[v.frame%len(spinner)]
	r := v.renderer(glyphs)
	now := v.o.Now()
	byID := map[string]prettylog.StepView{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	tail := 1
	if v.o.Verbosity >= prettylog.Verbose {
		tail = 10
	}
	var walk func(s prettylog.StepView)
	walk = func(s prettylog.StepView) {
		took := s.Took
		if s.State == prettylog.StepRunning && !s.Started.IsZero() {
			took = now.Sub(s.Started).Truncate(time.Second)
		}
		r.step(s, v.actors[s.ID], took, false)
		if s.State == prettylog.StepRunning {
			kids := children(steps, s.ID)
			if hidden := len(kids) - maxChildren; hidden > 0 {
				r.detail(s, "… "+strconv.Itoa(hidden)+" earlier steps")
				kids = kids[hidden:]
			}
			for _, k := range kids {
				walk(k)
			}
			if len(kids) == 0 {
				for _, line := range lastN(s.Tail, tail) {
					r.detail(s, line)
				}
			}
		}
	}
	for _, id := range v.active {
		if s, ok := byID[id]; ok {
			walk(s)
		}
	}
	return r.lines()
}

// summaryLocked is what an ended root step leaves in the scrollback: one
// collapsed line, plus, on failure, each failed sub-step with its last
// output lines and the count of earlier output bytes not kept.
func (v *View) summaryLocked(root prettylog.StepView, steps []prettylog.StepView) string {
	r := v.renderer(prettylog.DefaultGlyphs)
	r.step(root, v.actors[root.ID], root.Took, true)
	if root.State == prettylog.StepFailed || root.State == prettylog.StepUnknown {
		var walk func(parent string)
		walk = func(parent string) {
			for _, k := range children(steps, parent) {
				if k.State == prettylog.StepFailed {
					r.step(k, [2]string{}, k.Took, true)
					if k.Omitted > 0 {
						r.detail(k, "["+strconv.FormatInt(k.Omitted, 10)+" earlier bytes omitted]")
					}
					for _, line := range lastN(k.Tail, failedTail) {
						r.detail(k, line)
					}
				}
				walk(k.ID)
			}
		}
		walk(root.ID)
	}
	var b strings.Builder
	for _, line := range r.lines() {
		b.WriteString(line + "\r\n")
	}
	return b.String()
}

func lastN(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

// Lines is the writer for the Logger's other records: each write is shown
// above the block, with LF written as CRLF (safe in raw and cooked mode).
func (v *View) Lines() io.Writer { return lineWriter{v} }

type lineWriter struct{ v *View }

func (w lineWriter) Write(p []byte) (int, error) {
	return w.v.passthrough(w.v.out, crlf(p), len(p))
}

// Writer wraps another stream on the same terminal (raw stderr or stdout
// frames) so its bytes are written, unchanged, above the block. While such
// output ends mid-line, the block stays hidden.
func (v *View) Writer(w io.Writer) io.Writer {
	if v == nil {
		return w
	}
	return streamWriter{v, w}
}

type streamWriter struct {
	v *View
	w io.Writer
}

func (s streamWriter) Write(p []byte) (int, error) { return s.v.passthrough(s.w, p, len(p)) }

func (v *View) passthrough(w io.Writer, p []byte, n int) (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var b bytes.Buffer
	v.clearLocked(&b)
	if b.Len() > 0 {
		_, _ = v.out.Write(b.Bytes())
	}
	written, err := w.Write(p)
	if len(p) > 0 && written > 0 {
		v.partial = p[written-1] != '\n'
	}
	b.Reset()
	v.drawLocked(&b, v.tracker.Steps())
	if b.Len() > 0 {
		_, _ = v.out.Write(b.Bytes())
	}
	if err != nil {
		return min(written, n), err
	}
	return n, nil
}

func crlf(p []byte) []byte {
	if !bytes.Contains(p, []byte{'\n'}) {
		return p
	}
	out := make([]byte, 0, len(p)+8)
	for i, c := range p {
		if c == '\n' && (i == 0 || p[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, c)
	}
	return out
}

// Suspend erases the block and stops redrawing until the matching Resume,
// so a prompt, an attach or an exec owns the terminal. Records keep
// updating the steps meanwhile.
func (v *View) Suspend() {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pause++
	var b bytes.Buffer
	v.clearLocked(&b)
	if b.Len() > 0 {
		_, _ = v.out.Write(b.Bytes())
	}
	v.tickLocked()
}

// Resume undoes one Suspend. After the last one it writes the summaries of
// root steps that ended meanwhile and redraws the block.
func (v *View) Resume() {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.pause > 0 {
		v.pause--
	}
	if v.pause == 0 {
		v.partial = false
		v.updateLocked(false)
	}
}

// unfinished is the error shown for a root step still running at Close.
const unfinished = "did not finish before the command ended"

// Close leaves the current steps in the scrollback (a root step still
// running is shown as uncertain: "? Title" with "did not finish before the
// command ended"), stops redrawing and, for an installed View, restores the
// Logger's writer. Close ends any Suspend and
// any snapshot replay: the process is finishing, so pending summaries are
// written rather than lost.
func (v *View) Close() {
	if v == nil {
		return
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	var b bytes.Buffer
	v.clearLocked(&b)
	steps := v.tracker.Steps()
	byID := map[string]prettylog.StepView{}
	for _, s := range steps {
		byID[s.ID] = s
	}
	for _, id := range v.active {
		root := byID[id]
		if root.State == prettylog.StepRunning {
			// The command ended before this step reported an end: say so,
			// never show it as running or done.
			root.State, root.Err = prettylog.StepUnknown, unfinished
		}
		b.WriteString(v.summaryLocked(root, steps))
	}
	v.active = nil
	v.closed = true
	if b.Len() > 0 {
		_, _ = v.out.Write(b.Bytes())
	}
	v.tickLocked()
	restore := v.restore
	v.mu.Unlock()
	if restore != nil {
		restore()
	}
	current.CompareAndSwap(v, nil)
}
