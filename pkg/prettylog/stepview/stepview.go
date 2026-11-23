// Package stepview is the interactive inline steps block: while root steps
// run, it keeps a short live block at the bottom of a terminal (spinner,
// elapsed time, the running step's latest output) and prints other lines
// above it. Failed or uncertain children print as they end. Successful and
// skipped children stay in the live block until their parent can be printed.
//
// A View never reads the terminal and never owns the keyboard. It is driven
// by the step records of one prettylog.Logger (Install routes them to it) and
// renders through the same line grammar, glyphs and palette as the Logger.
package stepview

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"iter"
	"log/slog"
	"slices"
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
	// Label and HideField are the Logger's presentation hooks; the block
	// passes them to every renderer it creates.
	Label     func(actor string) (place, name string)
	HideField func(key, value string) bool
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
	// Pointer, when set, may name where an ended root step's output
	// is kept; it is asked only when the root or a sub-step had output
	// lines, and its non-empty answer is printed after the summary group.
	// actor is the root's actor. It must not log.
	Pointer func(actor string, root prettylog.StepView) string
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
	// Child IDs are bounded by the Tracker. A zero completion order is a
	// snapshot, which restores history without pretending the child just ended.
	settled   map[string]int
	committed map[string]bool
	pending   []string
	shown     int  // block rows currently on screen
	partial   bool // passthrough output left the cursor mid-line
	pause     int  // Suspend depth
	// replaying is set while a snapshot replay is in progress: its records
	// restore steps one by one, so ended roots are summarized only once the
	// replay ends (a step-less snapshot record or any live record).
	replaying bool
	closed    bool
	// owners maps each block row on screen (top first) to the step whose
	// title or output it shows; expanded is the running step shown with as
	// many of its kept lines as fit (Click toggles it).
	owners   []string
	expanded string
	frame    int
	dirty    bool
	ticker   *time.Ticker
	stop     chan struct{}
	restore  func()
}

// New returns a View writing to out (a terminal).
func New(out io.Writer, o Options) *View {
	if o.Interval <= 0 {
		o.Interval = 100 * time.Millisecond
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &View{out: out, o: o, tracker: prettylog.NewTracker(prettylog.TrackerLimits{}), actors: map[string][2]string{}, settled: map[string]int{}, committed: map[string]bool{}}
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
		for _, s := range v.tracker.Steps() {
			if s.ID != st.Step {
				continue
			}
			if s.Parent == "" {
				if _, seen := v.actors[s.ID]; !seen {
					v.actors[s.ID] = [2]string{st.Actor, st.Executor}
					v.active = append(v.active, s.ID)
				}
			} else if _, seen := v.settled[s.ID]; s.State != prettylog.StepRunning && !seen {
				v.settled[s.ID] = 0
				if !st.Snapshot {
					v.settled[s.ID] = len(v.settled)
					v.pending = append(v.pending, s.ID)
				}
			}
			break
		}
	}
	v.updateLocked(st.Detail && st.State == "")
	return applied
}

// updateLocked flushes terminal steps to the scrollback and redraws. A
// detail line alone only marks the block dirty (the ticker redraws it), so
// a burst of executor output costs at most one redraw per interval.
//
// Suspension, snapshot replay and a partial raw line defer completion output.
// Successful and skipped children wait for their parents' final lines.
func (v *View) updateLocked(detailOnly bool) {
	if v.closed {
		return
	}
	if v.pause > 0 || v.replaying || v.partial {
		v.dirty = true
		return
	}
	// A detail line with every active root still running only marks the
	// block dirty: read the root states without copying every step.
	if detailOnly && !v.tracker.AnyEnded(v.active) {
		v.dirty = true
		return
	}
	x := indexSteps(v.tracker.Steps())
	var ended []string
	kept := v.active[:0]
	for _, id := range v.active {
		if s, ok := x.lookup(id); ok && s.State != prettylog.StepRunning {
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
		root, _ := x.lookup(id)
		b.WriteString(v.summaryLocked(root, &x))
	}
	v.flushChildrenLocked(&b, &x)
	v.drawLocked(&b, &x)
	_, _ = v.out.Write(b.Bytes())
	v.tickLocked()
}

// flushChildrenLocked reports failed or uncertain children and handles late
// completions once their parents have printed.
func (v *View) flushChildrenLocked(b *bytes.Buffer, x *stepIndex) {
	kept := v.pending[:0]
	for _, id := range v.pending {
		if v.committed[id] {
			continue
		}
		s, _ := x.lookup(id)
		if s.State != prettylog.StepRunning && (failedOrUnknown(s.State) || v.committed[s.Parent]) {
			if _, rooted := x.parentChain(s); rooted {
				b.WriteString(v.summaryLocked(s, x))
				continue
			}
		}
		kept = append(kept, id)
	}
	v.pending = kept
}

func failedOrUnknown(state prettylog.StepState) bool {
	return state == prettylog.StepFailed || state == prettylog.StepUnknown
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
				// While raw output ends mid-line the block stays hidden:
				// drawLocked would draw nothing, so take no step copy.
				if !v.partial {
					x := indexSteps(v.tracker.Steps())
					v.drawLocked(&b, &x)
				}
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

func (v *View) drawLocked(b *bytes.Buffer, x *stepIndex) {
	if v.pause > 0 || v.closed || v.partial || len(v.active) == 0 {
		return
	}
	width, height := v.size()
	limit := max(height-2, 1)
	rows, owners := v.blockRows(x, limit)
	if len(rows) > limit {
		rows, owners = rows[len(rows)-limit:], owners[len(owners)-limit:]
	}
	for i, row := range rows {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(ansi.Truncate(row, max(width-1, 1), "…"))
	}
	v.shown = len(rows)
	v.owners = owners
}

// Click toggles the expansion of the running step shown on a block row:
// offset is that row's distance from the cursor row, which the block's last
// row occupies (0 is that row, -1 the one above). A row that is not part of
// the block, or a step that shows no output lines, is ignored. Clicks come
// from a TUI over a private channel; the View still reads no terminal.
func (v *View) Click(offset int) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	i := len(v.owners) - 1 + offset
	if v.closed || v.pause > 0 || v.shown == 0 || offset > 0 || i < 0 || i >= len(v.owners) || v.owners[i] == "" {
		return
	}
	if v.expanded == v.owners[i] {
		v.expanded = ""
	} else {
		v.expanded = v.owners[i]
	}
	var b bytes.Buffer
	v.clearLocked(&b)
	x := indexSteps(v.tracker.Steps())
	v.drawLocked(&b, &x)
	_, _ = v.out.Write(b.Bytes())
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
		Label: v.o.Label, HideField: v.o.HideField,
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

// stepIndex is one Tracker snapshot with its step IDs and parent links
// resolved once, so walking the steps does not search them again for each
// step. Tracker step IDs are unique. Children keep tracker (start) order;
// callers that sort siblings sort their own copy.
type stepIndex struct {
	steps []prettylog.StepView
	pos   map[string]int // step ID → position in steps; nil: scan steps
	// first and next link each step's children in start order: position+1
	// of a step's first child and of a step's next sibling (0 = none).
	first, next []int
}

// scanSteps is the largest snapshot searched without an index: one map
// group's worth of steps, where building the index would cost more
// allocations than the searches it saves.
const scanSteps = 8

func indexSteps(steps []prettylog.StepView) stepIndex {
	x := stepIndex{steps: steps}
	if len(steps) <= scanSteps {
		return x
	}
	x.pos = make(map[string]int, len(steps))
	for i, s := range steps {
		if _, seen := x.pos[s.ID]; !seen {
			x.pos[s.ID] = i
		}
	}
	links := make([]int, 2*len(steps))
	x.first, x.next = links[:len(steps)], links[len(steps):]
	for i := len(steps) - 1; i >= 0; i-- {
		if p, ok := x.pos[steps[i].Parent]; ok {
			x.next[i] = x.first[p]
			x.first[p] = i + 1
		}
	}
	return x
}

// lookup returns the first step with this ID.
func (x *stepIndex) lookup(id string) (prettylog.StepView, bool) {
	if x.pos == nil {
		for _, s := range x.steps {
			if s.ID == id {
				return s, true
			}
		}
	} else if i, ok := x.pos[id]; ok {
		return x.steps[i], true
	}
	return prettylog.StepView{}, false
}

// children yields the steps whose parent is parent, in start order.
func (x *stepIndex) children(parent string) iter.Seq[prettylog.StepView] {
	return func(yield func(prettylog.StepView) bool) {
		p, ok := x.pos[parent]
		if !ok {
			// No index, or not a step of this snapshot (such as ""): scan.
			for _, s := range x.steps {
				if s.Parent == parent && !yield(s) {
					return
				}
			}
			return
		}
		for i := x.first[p]; i != 0; i = x.next[i-1] {
			if !yield(x.steps[i-1]) {
				return
			}
		}
	}
}

// liveChildren keeps the existing running-step slots. Recent completions
// fill the slots left over, so an active child stays visible as siblings end.
func (v *View) liveChildren(x *stepIndex, parent string, showCompleted bool) ([]prettylog.StepView, int, bool) {
	var running, completed []prettylog.StepView
	for child := range x.children(parent) {
		if child.State == prettylog.StepRunning {
			running = append(running, child)
		} else if showCompleted && v.settled[child.ID] > 0 && !v.committed[child.ID] {
			completed = append(completed, child)
		}
	}
	count := len(running) + len(completed)
	running = lastN(running, maxChildren)
	completed = lastN(completed, maxChildren-len(running))
	return append(completed, running...), max(count-maxChildren, 0), len(running) > 0
}

// blockRows renders the live block: each active root, then its most recent
// sub-steps (older ones counted), each running one with its last output,
// and the step each row belongs to ("" for a row that expands nothing). The
// expanded step shows as many of its latest kept lines as fit in limit
// rows with the rest of the block, counting the earlier ones on one row.
func (v *View) blockRows(x *stepIndex, limit int) ([]string, []string) {
	showCompleted := true
	rows, owners := v.blockRowsWith(x, showCompleted, nil)
	if len(rows) > limit {
		// Preserve the running rows when the terminal is too short for
		// both running and completed steps.
		showCompleted = false
		rows, owners = v.blockRowsWith(x, showCompleted, nil)
	}
	if v.expanded == "" {
		return rows, owners
	}
	var lines []string
	for _, s := range v.tracker.Retained() {
		if s.ID == v.expanded && s.State == prettylog.StepRunning {
			lines = s.Tail
		}
	}
	shown := 0
	for _, o := range owners {
		if o == v.expanded {
			shown++
		}
	}
	if shown == 0 || len(lines) <= 1 {
		return rows, owners
	}
	// The step's own rows are its title and its tail; the rest stays.
	room := max(limit-(len(rows)-shown)-1, 1)
	return v.blockRowsWith(x, showCompleted, func(s prettylog.StepView) []string {
		if s.ID != v.expanded {
			return nil
		}
		if len(lines) <= room {
			return lines
		}
		if room < 2 {
			// No row for the count: the latest line only, as collapsed.
			return lines[len(lines)-room:]
		}
		keep := room - 1
		return append([]string{"… " + strconv.Itoa(len(lines)-keep) + " earlier lines"}, lines[len(lines)-keep:]...)
	})
}

// blockRowsWith renders the block; expand, when it returns lines for a
// running step, replaces that step's short tail.
func (v *View) blockRowsWith(x *stepIndex, showCompleted bool, expand func(prettylog.StepView) []string) ([]string, []string) {
	glyphs := prettylog.DefaultGlyphs
	glyphs.Info = spinner[v.frame%len(spinner)]
	r := v.renderer(glyphs)
	now := v.o.Now()
	tail := 1
	if v.o.Verbosity >= prettylog.Verbose {
		tail = 10
	}
	// Each row rendered since the last call belongs to id. The renderer only
	// appends, so only the bytes after the previous count are counted.
	var owners []string
	counted, rows := 0, 0
	own := func(id string) {
		rendered := r.buf.Bytes()
		rows += bytes.Count(rendered[counted:], []byte{'\n'})
		counted = len(rendered)
		for len(owners) < rows {
			owners = append(owners, id)
		}
	}
	var walk func(s prettylog.StepView)
	walk = func(s prettylog.StepView) {
		took := s.Took
		if s.State == prettylog.StepRunning && !s.Started.IsZero() {
			took = now.Sub(s.Started).Truncate(time.Second)
		}
		r.step(s, v.actors[s.ID], took, false)
		// A running leaf shows output lines and can expand: its rows are its.
		kids, hidden, hasRunningChildren := v.liveChildren(x, s.ID, showCompleted)
		leaf := s.State == prettylog.StepRunning && !hasRunningChildren
		if leaf {
			own(s.ID)
		} else {
			own("")
		}
		if hidden > 0 {
			r.detail(s, "… "+strconv.Itoa(hidden)+" earlier steps")
			own("")
		}
		for _, k := range kids {
			walk(k)
		}
		if leaf {
			lines := lastN(s.Tail, tail)
			if expand != nil {
				if more := expand(s); more != nil {
					lines = more
				}
			}
			for _, line := range lines {
				r.detail(s, line)
			}
			own(s.ID)
		}
	}
	for _, id := range v.active {
		if s, ok := x.lookup(id); ok {
			walk(s)
		}
	}
	return r.lines(), owners
}

// summaryLocked formats a finished group in parent-first order. Siblings
// keep their completion order; snapshot-only failures follow in tracker order.
func (v *View) summaryLocked(root prettylog.StepView, x *stepIndex) string {
	r := v.renderer(prettylog.DefaultGlyphs)
	r.seedParents(root, x, v.actors)
	var walk func(s prettylog.StepView, write, restoreFailures bool)
	walk = func(s prettylog.StepView, write, restoreFailures bool) {
		failed := failedOrUnknown(s.State)
		if write && !v.committed[s.ID] {
			r.step(s, v.actors[s.ID], s.Took, true)
			v.committed[s.ID] = true
			if s.Parent != "" && failed {
				failureDetails(r, s)
			}
		} else {
			// Seed the indentation for diagnostics under a hidden step.
			kept := r.buf.Len()
			r.step(s, v.actors[s.ID], s.Took, false)
			r.buf.Truncate(kept)
		}
		restoreFailures = restoreFailures || failed
		kids := slices.Collect(x.children(s.ID))
		slices.SortStableFunc(kids, func(a, b prettylog.StepView) int {
			orderA, orderB := v.settled[a.ID], v.settled[b.ID]
			if orderA == 0 || orderB == 0 {
				return cmp.Compare(orderB, orderA)
			}
			return cmp.Compare(orderA, orderB)
		})
		for _, k := range kids {
			order := v.settled[k.ID]
			if v.committed[s.ID] && k.State != prettylog.StepRunning && order > 0 {
				walk(k, true, restoreFailures)
			} else if restoreFailures {
				// Preserve diagnostics from a failed root's snapshot, even
				// when intermediate steps have no printable completion.
				walk(k, order == 0 && failedOrUnknown(k.State), true)
			}
		}
	}
	walk(root, true, false)
	if root.Parent == "" && v.o.Pointer != nil && hadOutput(root, x) {
		actor := v.actors[root.ID]
		if text := v.o.Pointer(actor[0], root); text != "" {
			rec := slog.NewRecord(time.Time{}, prettylog.LevelInfo, text, 0)
			if actor[0] != "" {
				rec.AddAttrs(prettylog.Actor(actor[0]))
			}
			_ = r.l.Handle(context.Background(), rec)
		}
	}
	var b strings.Builder
	for _, line := range r.lines() {
		b.WriteString(line + "\r\n")
	}
	return b.String()
}

// seedParents restores formatting depth without emitting ancestor rows again.
// Preserve any summary rows already formatted before seeding another branch.
func (r *renderer) seedParents(s prettylog.StepView, x *stepIndex, actors map[string][2]string) {
	parents, _ := x.parentChain(s)
	kept := r.buf.Len()
	for i := len(parents) - 1; i >= 0; i-- {
		r.step(parents[i], actors[parents[i].ID], parents[i].Took, false)
	}
	r.buf.Truncate(kept)
}

// parentChain is bounded by the Tracker's existing step inventory. Orphan or
// cyclic child records have no known root and never acquire scrollback ownership.
func (x *stepIndex) parentChain(s prettylog.StepView) ([]prettylog.StepView, bool) {
	var parents []prettylog.StepView
	parent := s.Parent
	for range len(x.steps) {
		if parent == "" {
			return parents, true
		}
		p, ok := x.lookup(parent)
		if !ok {
			return nil, false
		}
		parents = append(parents, p)
		parent = p.Parent
	}
	return parents, parent == ""
}

func failureDetails(r *renderer, s prettylog.StepView) {
	if s.Omitted > 0 {
		r.detail(s, "["+strconv.FormatInt(s.Omitted, 10)+" earlier bytes omitted]")
	}
	for _, line := range lastN(s.Tail, failedTail) {
		r.detail(s, line)
	}
}

// hadOutput reports whether root or any step under it had output lines.
func hadOutput(root prettylog.StepView, x *stepIndex) bool {
	if len(root.Tail) > 0 || root.Omitted > 0 {
		return true
	}
	for k := range x.children(root.ID) {
		if hadOutput(k, x) {
			return true
		}
	}
	return false
}

func lastN[T any](lines []T, n int) []T {
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
	v.updateLocked(false)
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
// steps that ended meanwhile and redraws the block.
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
	x := indexSteps(v.tracker.Steps())
	for _, id := range v.active {
		root, _ := x.lookup(id)
		if root.State == prettylog.StepRunning {
			// The command ended before this step reported an end: say so,
			// never show it as running or done.
			root.State, root.Err = prettylog.StepUnknown, unfinished
		}
		b.WriteString(v.summaryLocked(root, &x))
	}
	v.flushChildrenLocked(&b, &x)
	// An intermediate parent that never finished has no final line. Keep
	// its observed descendants rather than dropping them at command exit.
	for _, id := range v.pending {
		s, _ := x.lookup(id)
		parents, ok := x.parentChain(s)
		if !ok || v.committed[id] || s.State == prettylog.StepRunning {
			continue
		}
		for _, p := range parents {
			if p.State == prettylog.StepRunning || v.settled[p.ID] == 0 || v.committed[p.ID] {
				break
			}
			s = p
		}
		b.WriteString(v.summaryLocked(s, &x))
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
