package prettylog

import (
	"context"
	"log/slog"
	"time"
)

// EmissionKind distinguishes a shown record from its eventual fold summary.
type EmissionKind uint8

const (
	EmissionRecord EmissionKind = iota
	EmissionRepeatSummary
)

// Emission is one presentation decision from the existing filter/fold engine.
// Text is the bounded formatted output, without a final newline. Record retains
// structured application input, so consumers displaying it must keep their own
// domain sanitization. A summary has no Record and owns the first shown event.
type Emission struct {
	Kind   EmissionKind
	Record slog.Record
	Owner  uint64
	Repeat int
	Until  time.Time
	Text   string
}

// EmissionSink is an alternative to the byte destination. Emit is synchronous
// under the Logger mutex, including timer/Close callbacks, and must not reenter
// the same Logger. Application/UI sinks must not block on their event loop.
type EmissionSink interface {
	Emit(Emission) error
}

// HandleOwned associates a local opaque identity with a record. Zero means
// absent. Identity never becomes a slog attr, fold key, rendered text or wire
// field; the active fold run retains only its first displayed identity.
func (l *Logger) HandleOwned(_ context.Context, r slog.Record, owner uint64) error {
	return l.emitOwned(r, owner)
}

func (l *Logger) publish(emission Emission) {
	if l.o.Emit == nil {
		l.write(emission.Text)
		return
	}
	if err := l.o.Emit.Emit(emission); err != nil && l.err == nil {
		l.err = err
	}
}
