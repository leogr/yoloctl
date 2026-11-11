package prettylog

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Format is how a Logger writes records.
type Format int

// Formats. FormatWire writes length-prefixed encoded records (see
// MarshalRecord) for another process to render; it never folds, colours or
// formats time.
const (
	FormatPretty Format = iota
	FormatPlain
	FormatWire
)

// TimeFormat selects the timestamp at the start of a line.
type TimeFormat int

// Time formats: none (interactive CLI), local clock (follow views) and UTC
// RFC 3339 with milliseconds (files).
const (
	TimeNone TimeFormat = iota
	TimeClock
	TimeRFC3339
)

// Verbosity is what a reader asked to see.
type Verbosity int

// Verbosities, from least to most. yoloctl's --log-level maps error, warn,
// info and debug to Errors, Quiet, Normal and Trace; Verbose (debug records
// without executor output) remains for other consumers.
const (
	Errors Verbosity = iota - 1
	Quiet
	Normal
	Verbose
	Trace
)

// Allows is the display predicate. Errors shows errors and uncertain results
// (failed and unknown outcomes included); Quiet adds warnings and every
// outcome; Normal adds notices, progress and step transitions; Verbose adds
// debug records; Trace adds step detail lines.
func (v Verbosity) Allows(r slog.Record) bool { return v.allows(StructureOf(r)) }

func (v Verbosity) allows(st Structure) bool {
	switch {
	case st.Detail && !st.transition(): // live or snapshot executor output
		return v >= Trace
	case st.Kind == KindDebug:
		return v >= Verbose
	case v >= Normal:
		return true
	case v <= Errors:
		return st.Kind == KindError || st.Kind == KindUnknown
	}
	return st.Kind == KindError || st.Kind == KindWarn || st.Kind == KindUnknown || st.Outcome != ""
}

// wireAllows keeps everything the host may need: step and snapshot records
// always pass, so the host's Tracker can show a failed step's output.
func (v Verbosity) wireAllows(st Structure) bool {
	return st.Step != "" || st.Snapshot || st.transition() || st.Detail || v.allows(st)
}

// FoldOptions configures folding of consecutive identical records.
type FoldOptions struct {
	// Window is the longest gap between two folded records; 0 disables
	// folding.
	Window time.Duration
	// Key identifies records that fold together; nil uses DefaultFoldKey. A
	// false result means the record never folds.
	Key func(slog.Record) (string, bool)
}

// DefaultFoldKey is the level, the message and every attribute's key, kind
// and value. Each part is length-prefixed, so different records never share
// a key.
func DefaultFoldKey(r slog.Record) (string, bool) {
	var b strings.Builder
	part := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	part(strconv.Itoa(int(r.Level)))
	part(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		for _, f := range flatten(nil, "", a) {
			part(f.key)
			part(f.val.Kind().String())
			if f.val.Kind() == slog.KindAny {
				part(fmt.Sprintf("%T", f.val.Any()))
			}
			part(f.val.String())
		}
		return true
	})
	return b.String(), true
}

// Options configures a Logger.
type Options struct {
	Format     Format
	Color      bool    // decided once, by DecideColor
	Palette    Palette // initial palette; SetPalette replaces it
	Glyphs     Glyphs  // zero value uses DefaultGlyphs
	Verbosity  Verbosity
	Time       TimeFormat
	ActorWidth int      // pads the actor column; 0 = no padding
	Fields     []string // non-reserved fields shown at Normal; Verbose shows all
	Limits     Limits
	Fold       FoldOptions
	Now        func() time.Time // nil uses time.Now
}

// Logger owns one sink: its writer, its fold state and its step ids. It is
// synchronous: every record is written before Handle returns, under one mutex
// shared by every handler derived from it. It never closes its writer.
type Logger struct {
	w io.Writer
	o Options

	token   string
	counter atomic.Uint64

	mu      sync.Mutex
	out     io.Writer // w, or the routed writer
	steps   StepSink  // non-nil while routed
	pal     Palette
	err     error
	closed  bool
	run     *foldRun
	timer   *time.Timer
	gen     uint64
	depth   map[string]int
	outcome OutcomeKind
	hasOut  bool
}

// foldRun is the open run of identical records.
type foldRun struct {
	key  string
	n    int
	last time.Time
	col  int
	gen  uint64
}

// New returns a Logger writing to w.
func New(w io.Writer, o Options) *Logger {
	if o.Glyphs == (Glyphs{}) {
		o.Glyphs = DefaultGlyphs
	}
	if o.Fold.Key == nil {
		o.Fold.Key = DefaultFoldKey
	}
	if o.Format == FormatWire {
		o.Fold.Window, o.Color, o.Time = 0, false, TimeNone
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixNano()))
	}
	return &Logger{w: w, out: w, o: o, pal: o.Palette, token: hex.EncodeToString(b[:]), depth: map[string]int{}}
}

func (l *Logger) now() time.Time {
	if l.o.Now != nil {
		return l.o.Now()
	}
	return time.Now()
}

// Slog returns a slog front end for l.
func (l *Logger) Slog() *slog.Logger { return slog.New(l.Handler()) }

// Handler returns l's slog handler.
func (l *Logger) Handler() slog.Handler { return &handler{l: l} }

// Handle writes an already built record, such as one decoded from the wire.
func (l *Logger) Handle(_ context.Context, r slog.Record) error { return l.emit(r) }

// Outcome writes the final outcome of a command: ok at Info, failed at Error
// and unknown at Warn.
func (l *Logger) Outcome(k OutcomeKind, msg string, attrs ...slog.Attr) {
	level := LevelInfo
	switch k {
	case OutcomeFailed:
		level = LevelError
	case OutcomeUnknown:
		level = LevelWarn
	}
	r := slog.NewRecord(l.now(), level, msg, 0)
	r.AddAttrs(attrs...)
	r.AddAttrs(Outcome(k))
	_ = l.emit(r)
}

// LastOutcome is the last non-step outcome this Logger received, whether or
// not it was shown. It is a receipt: it proves neither display (the writer may
// have failed) nor any relation to a later error.
func (l *Logger) LastOutcome() (OutcomeKind, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.outcome, l.hasOut
}

// SetPalette replaces the palette from the next line on.
func (l *Logger) SetPalette(p Palette) {
	l.mu.Lock()
	l.pal = p
	l.mu.Unlock()
}

// StepSink receives step records while a Logger is routed. Apply runs under
// the Logger's mutex and must not call back into the same Logger.
type StepSink interface{ Apply(slog.Record) bool }

// Route sends every record with a step or snapshot attribute to steps,
// before display filtering, instead of formatting it, and formats all other
// records (fold summaries included) to w. restore puts the original writer
// back and ends the routing. FormatWire Loggers are not routed.
func (l *Logger) Route(w io.Writer, steps StepSink) (restore func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.o.Format == FormatWire {
		return func() {}
	}
	prevOut, prevSteps := l.out, l.steps
	l.out, l.steps = w, steps
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.out, l.steps = prevOut, prevSteps
	}
}

// Flush writes the pending fold summary now.
func (l *Logger) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.endRun()
	return l.err
}

// Close flushes and stops the fold timer. Later records are written without
// folding. It does not close the writer and may block like any write.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.endRun()
	if l.timer != nil {
		l.timer.Stop()
	}
	l.closed = true
	return l.err
}

// Err is the first write error, if any. A write error never changes what the
// caller does; later records still try to write.
func (l *Logger) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *Logger) emit(r slog.Record) error {
	st := StructureOf(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	if st.Outcome != "" && st.Step == "" {
		switch k := OutcomeKind(st.Outcome); k {
		case OutcomeOK, OutcomeFailed, OutcomeUnknown:
			l.outcome, l.hasOut = k, true
		}
	}
	if l.o.Format == FormatWire {
		if !l.o.Verbosity.wireAllows(st) {
			return nil
		}
		return l.writeWire(r)
	}
	if l.steps != nil && (st.Step != "" || st.Snapshot) {
		l.steps.Apply(r)
		return nil
	}
	if !l.o.Verbosity.allows(st) {
		return nil
	}
	if r.Time.IsZero() {
		r.Time = l.now()
	}
	if l.o.Fold.Window > 0 && !l.closed && foldable(st) {
		if key, ok := l.o.Fold.Key(r); ok {
			if run := l.run; run != nil && run.key == key && r.Time.Sub(run.last) <= l.o.Fold.Window && !r.Time.Before(run.last) {
				run.n++
				run.last = r.Time
				l.arm()
				return l.err
			}
			l.endRun()
			ln := l.render(r, st, l.depthOf(st))
			l.write(ln.text)
			l.run = &foldRun{key: key, last: r.Time, col: ln.col}
			l.arm()
			return l.err
		}
	}
	l.endRun()
	l.write(l.render(r, st, l.depthOf(st)).text)
	return l.err
}

func foldable(st Structure) bool {
	return st.Outcome == "" && st.Step == "" && st.State == "" && !st.Detail && !st.Snapshot
}

// arm (re)starts the fold timer for the open run. Called with l.mu held.
func (l *Logger) arm() {
	l.gen++
	l.run.gen = l.gen
	gen := l.gen
	if l.timer != nil {
		l.timer.Stop()
	}
	l.timer = time.AfterFunc(l.o.Fold.Window, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.run != nil && l.run.gen == gen {
			l.endRun()
		}
	})
}

// endRun writes the open run's summary, if it folded anything. Called with
// l.mu held.
func (l *Logger) endRun() {
	run := l.run
	l.run = nil
	if run == nil || run.n == 0 {
		return
	}
	l.write(l.summary(run))
}

// depthOf is a step record's nesting depth, remembered per step id. The map
// is bounded: past maxDepthIDs ids it starts again (depth then falls back to
// one level under an unknown parent).
const maxDepthIDs = 4096

func (l *Logger) depthOf(st Structure) int {
	if st.Step == "" {
		return 0
	}
	if st.Detail && !st.transition() {
		return l.depth[st.Step] + 1
	}
	d := 0
	if st.Parent != "" {
		pd, ok := l.depth[st.Parent]
		d = 1
		if ok {
			d = pd + 1
		}
	}
	if len(l.depth) >= maxDepthIDs {
		clear(l.depth)
	}
	l.depth[st.Step] = d
	return d
}

func (l *Logger) write(text string) {
	if _, err := io.WriteString(l.out, text+"\n"); err != nil && l.err == nil {
		l.err = err
	}
}

func (l *Logger) nextID() string {
	return l.token + "." + strconv.FormatUint(l.counter.Add(1), 10)
}

// handler is the slog front end. Attributes added with WithAttrs come before
// a record's own attributes, so a record's reserved keys win; groups flatten
// to dotted keys, so reserved keys inside a group are ordinary fields.
type handler struct {
	l      *Logger
	pre    []slog.Attr
	prefix string
}

func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	if len(h.pre) == 0 && h.prefix == "" {
		return h.l.emit(r)
	}
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	out.AddAttrs(h.pre...)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.qualify(a)...)
		return true
	})
	return h.l.emit(out)
}

func (h *handler) qualify(a slog.Attr) []slog.Attr {
	if h.prefix == "" {
		return []slog.Attr{a}
	}
	var out []slog.Attr
	for _, f := range flatten(nil, h.prefix, a) {
		out = append(out, slog.Attr{Key: f.key, Value: f.val})
	}
	return out
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := &handler{l: h.l, prefix: h.prefix, pre: append([]slog.Attr(nil), h.pre...)}
	for _, a := range attrs {
		n.pre = append(n.pre, h.qualify(a)...)
	}
	return n
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &handler{l: h.l, pre: h.pre, prefix: h.prefix + name + "."}
}
