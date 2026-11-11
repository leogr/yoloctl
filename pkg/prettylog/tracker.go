package prettylog

import (
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"
)

// TailLines is how many retained detail lines a StepView shows.
const TailLines = 20

// StepView is a step as a Tracker knows it.
type StepView struct {
	ID, Parent, Title string
	Attrs             []slog.Attr // non-reserved attributes; a later value replaces an earlier one
	State             StepState
	Started           time.Time
	Took              time.Duration
	Err, Reason       string
	Tail              []string // last TailLines retained detail lines
	Omitted           int64    // detail bytes (each line with its newline) of this step not in Tail
}

// TrackerLimits bounds a Tracker. Zero values take the defaults: 200 steps,
// 64 KiB of detail per step and 1 MiB in total.
type TrackerLimits struct{ Steps, StepDetail, TotalDetail int }

func (l TrackerLimits) withDefaults() TrackerLimits {
	if l.Steps <= 0 {
		l.Steps = 200
	}
	if l.StepDetail <= 0 {
		l.StepDetail = 64 << 10
	}
	if l.TotalDetail <= 0 {
		l.TotalDetail = 1 << 20
	}
	return l
}

// Tracker reduces step records to the current state of every step. It is
// bounded, safe for concurrent use and fed with every received record before
// display filtering.
type Tracker struct {
	mu       sync.Mutex
	lim      TrackerLimits
	steps    map[string]*tracked
	order    []*tracked
	overflow int
	total    int
}

type tracked struct {
	view    StepView
	lines   []string
	bytes   int
	dropped int64
}

func (t *tracked) ended() bool { return t.view.State != StepRunning }

// NewTracker returns an empty Tracker.
func NewTracker(lim TrackerLimits) *Tracker {
	return &Tracker{lim: lim.withDefaults(), steps: map[string]*tracked{}}
}

// Apply reduces one record. It reports whether r belongs to the step stream
// (a step, detail or snapshot record), changed or not.
func (t *Tracker) Apply(r slog.Record) bool {
	st := StructureOf(r)
	t.mu.Lock()
	defer t.mu.Unlock()
	if st.Step == "" {
		if st.Snapshot && st.Overflow > 0 {
			t.overflow = st.Overflow
			return true
		}
		return false
	}
	ts := t.steps[st.Step]
	switch {
	case st.Snapshot && st.State != "":
		if ts == nil {
			if ts = t.add(st, r); ts == nil {
				return true
			}
		}
		t.restore(ts, st, r)
	case st.State != "":
		if ts == nil {
			if len(t.order) >= t.lim.Steps {
				if st.State == string(StepRunning) {
					t.overflow++
					return true
				}
				return false
			}
			ts = t.add(st, r)
			started := r.Time
			if st.State != string(StepRunning) {
				started = r.Time.Add(-st.Took)
			}
			ts.view.Started = started
			t.transition(ts, st, r)
			return true
		}
		if ts.ended() || st.State == string(StepRunning) {
			return true // states only move forward
		}
		t.transition(ts, st, r)
	case st.Detail:
		if ts == nil {
			return false
		}
		t.addLine(ts, r.Message)
	default:
		return ts != nil
	}
	return true
}

// add starts tracking a step, or counts it as overflow (returning nil).
func (t *Tracker) add(st Structure, r slog.Record) *tracked {
	if len(t.order) >= t.lim.Steps {
		t.overflow++
		return nil
	}
	ts := &tracked{view: StepView{ID: st.Step, Parent: st.Parent, Title: r.Message, State: StepRunning}}
	t.steps[st.Step] = ts
	t.order = append(t.order, ts)
	return ts
}

func (t *Tracker) transition(ts *tracked, st Structure, r slog.Record) {
	v := &ts.view
	v.State = StepState(st.State)
	switch v.State {
	case StepRunning, StepOK, StepFailed, StepUnknown, StepSkipped:
	default:
		v.State = StepRunning
	}
	if st.Parent != "" {
		v.Parent = st.Parent
	}
	if v.State != StepRunning {
		v.Took, v.Err, v.Reason = st.Took, st.Err, st.Reason
	}
	v.Attrs = mergeAttrs(v.Attrs, r)
}

// restore replaces a step's whole view from a snapshot record.
func (t *Tracker) restore(ts *tracked, st Structure, r slog.Record) {
	t.total -= ts.bytes
	ts.lines, ts.bytes, ts.dropped = nil, 0, st.Omitted
	ts.view = StepView{
		ID: st.Step, Parent: st.Parent, Title: r.Message, State: StepState(st.State),
		Started: st.Started, Took: st.Took, Err: st.Err, Reason: st.Reason,
		Attrs: mergeAttrs(nil, r),
	}
}

func mergeAttrs(dst []slog.Attr, r slog.Record) []slog.Attr {
	eachAttr(r, func(a slog.Attr, role attrRole) bool {
		if role != roleField {
			return true
		}
		a.Value = a.Value.Resolve()
		if i := slices.IndexFunc(dst, func(b slog.Attr) bool { return b.Key == a.Key }); i >= 0 {
			dst[i] = a
		} else {
			dst = append(dst, a)
		}
		return true
	})
	return dst
}

// lineCost is a retained line's size: its bytes plus the newline it ended
// with, so empty lines are bounded too.
func lineCost(text string) int { return len(text) + 1 }

func (t *Tracker) addLine(ts *tracked, text string) {
	ts.lines = append(ts.lines, text)
	ts.bytes += lineCost(text)
	t.total += lineCost(text)
	for ts.bytes > t.lim.StepDetail && len(ts.lines) > 0 {
		t.dropOldest(ts)
	}
	for t.total > t.lim.TotalDetail {
		victim := t.victim()
		if victim == nil {
			break
		}
		t.dropOldest(victim)
	}
}

// victim is the earliest-started ended step with retained lines, else the
// earliest-started running one.
func (t *Tracker) victim() *tracked {
	var running *tracked
	for _, ts := range t.order {
		if len(ts.lines) == 0 {
			continue
		}
		if ts.ended() {
			return ts
		}
		if running == nil {
			running = ts
		}
	}
	return running
}

func (t *Tracker) dropOldest(ts *tracked) {
	n := lineCost(ts.lines[0])
	ts.lines[0] = ""
	ts.lines = ts.lines[1:]
	ts.bytes -= n
	t.total -= n
	ts.dropped += int64(n)
}

func (ts *tracked) snapshotView() StepView {
	v := ts.view
	v.Attrs = slices.Clone(v.Attrs)
	tail := ts.lines
	if len(tail) > TailLines {
		tail = tail[len(tail)-TailLines:]
	}
	v.Tail = slices.Clone(tail)
	shown := 0
	for _, l := range tail {
		shown += lineCost(l)
	}
	v.Omitted = ts.dropped + int64(ts.bytes-shown)
	return v
}

// Steps returns every tracked step in start order.
func (t *Tracker) Steps() []StepView {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]StepView, 0, len(t.order))
	for _, ts := range t.order {
		out = append(out, ts.snapshotView())
	}
	return out
}

// Overflow is how many steps were not tracked because of the Steps limit.
func (t *Tracker) Overflow() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.overflow
}

// Snapshot returns records that rebuild this Tracker's Steps and Overflow in
// an empty Tracker with the same limits: one restoring record per step in
// start order, then that step's tail lines, then the overflow count.
func (t *Tracker) Snapshot() []slog.Record {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []slog.Record
	for _, ts := range t.order {
		v := ts.snapshotView()
		at := v.Started
		level := LevelInfo
		switch v.State {
		case StepFailed:
			level = LevelError
		case StepUnknown:
			level = LevelWarn
		}
		if v.State != StepRunning {
			at = at.Add(v.Took)
		}
		r := slog.NewRecord(at, level, v.Title, 0)
		r.AddAttrs(v.Attrs...)
		r.AddAttrs(slog.Bool(KeySnapshot, true), slog.String(KeyStep, v.ID))
		if v.Parent != "" {
			r.AddAttrs(slog.String(KeyParent, v.Parent))
		}
		r.AddAttrs(slog.String(KeyState, string(v.State)), slog.Time(KeyStarted, v.Started),
			slog.Int64(KeyOmitted, v.Omitted))
		if v.Took != 0 {
			r.AddAttrs(Took(v.Took))
		}
		if v.Err != "" {
			r.AddAttrs(slog.String(KeyErr, v.Err))
		}
		if v.Reason != "" {
			r.AddAttrs(slog.String(KeyReason, v.Reason))
		}
		out = append(out, r)
		for _, line := range v.Tail {
			d := slog.NewRecord(at, LevelDebug, line, 0)
			d.AddAttrs(slog.Bool(KeySnapshot, true), slog.String(KeyStep, v.ID), slog.Bool(KeyDetail, true))
			out = append(out, d)
		}
	}
	if t.overflow > 0 {
		r := slog.NewRecord(time.Time{}, LevelDebug, "+"+strconv.Itoa(t.overflow)+" steps", 0)
		r.AddAttrs(slog.Bool(KeySnapshot, true), slog.Int(KeyOverflow, t.overflow))
		out = append(out, r)
	}
	return out
}
