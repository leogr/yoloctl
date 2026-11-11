package prettylog

import (
	"bytes"
	"io"
	"log/slog"
	"sync"
	"time"
)

// StepState is where a step is in its life: running, then one end state.
type StepState string

// Step states.
const (
	StepRunning StepState = "running"
	StepOK      StepState = "ok"
	StepFailed  StepState = "failed"
	StepUnknown StepState = "unknown"
	StepSkipped StepState = "skipped"
)

// Step is one unit of a long operation. Each transition is one record, so any
// handler or a Tracker can show it. A step emits running once and then at
// most one end state; later end calls do nothing.
type Step struct {
	l      *Logger
	id     string
	parent string
	title  string
	attrs  []slog.Attr
	start  time.Time

	mu    sync.Mutex
	ended bool
	buf   []byte
}

// StartStep starts a root step. Root steps carry the actor and executor.
func (l *Logger) StartStep(title string, attrs ...slog.Attr) *Step {
	return l.startStep("", title, attrs)
}

// Step starts a child step.
func (s *Step) Step(title string, attrs ...slog.Attr) *Step {
	return s.l.startStep(s.id, title, attrs)
}

func (l *Logger) startStep(parent, title string, attrs []slog.Attr) *Step {
	s := &Step{l: l, id: l.nextID(), parent: parent, title: title, attrs: attrs, start: l.now()}
	s.emit(s.start, LevelInfo, StepRunning)
	return s
}

// ID is the step's id, unique within a stream with high probability.
func (s *Step) ID() string { return s.id }

func (s *Step) emit(t time.Time, level slog.Level, state StepState, extra ...slog.Attr) {
	r := slog.NewRecord(t, level, s.title, 0)
	r.AddAttrs(s.attrs...)
	r.AddAttrs(extra...)
	r.AddAttrs(slog.String(KeyStep, s.id))
	if s.parent != "" {
		r.AddAttrs(slog.String(KeyParent, s.parent))
	}
	r.AddAttrs(slog.String(KeyState, string(state)))
	_ = s.l.emit(r)
}

// Detail is the step's executor output. Each line becomes one detail record,
// which a transport may drop and which shows only at Trace.
func (s *Step) Detail() io.Writer { return detailWriter{s} }

type detailWriter struct{ s *Step }

func (w detailWriter) Write(p []byte) (int, error) {
	s := w.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return len(p), nil
	}
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		s.detail(string(bytes.TrimSuffix(s.buf[:i], []byte("\r"))))
		s.buf = s.buf[i+1:]
	}
	if len(s.buf) > MaxWire {
		s.detail(string(s.buf))
		s.buf = nil
	}
	return len(p), nil
}

// detail emits one detail line. Called with s.mu held.
func (s *Step) detail(text string) {
	r := slog.NewRecord(s.l.now(), LevelDebug, text, 0)
	r.AddAttrs(slog.String(KeyStep, s.id), slog.Bool(KeyDetail, true))
	_ = s.l.emit(r)
}

func (s *Step) end(level slog.Level, state StepState, extra ...slog.Attr) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	if len(s.buf) > 0 {
		s.detail(string(s.buf))
		s.buf = nil
	}
	s.mu.Unlock()
	now := s.l.now()
	if state != StepSkipped {
		extra = append(extra, Took(now.Sub(s.start)))
	}
	s.emit(now, level, state, extra...)
}

// Done ends the step successfully.
func (s *Step) Done(attrs ...slog.Attr) { s.end(LevelInfo, StepOK, attrs...) }

// Fail ends the step with a failure.
func (s *Step) Fail(err error, attrs ...slog.Attr) {
	s.end(LevelError, StepFailed, append(attrs, Err(err))...)
}

// Unknown ends the step with an unproven result; err carries the existing
// uncertain-outcome wording verbatim.
func (s *Step) Unknown(err error, attrs ...slog.Attr) {
	s.end(LevelWarn, StepUnknown, append(attrs, Err(err))...)
}

// Skip ends the step without running it.
func (s *Step) Skip(reason string) {
	var extra []slog.Attr
	if reason != "" {
		extra = append(extra, slog.String(KeyReason, reason))
	}
	s.end(LevelInfo, StepSkipped, extra...)
}
