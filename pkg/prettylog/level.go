// Package prettylog renders log/slog records as short, themed terminal lines
// or plain text, folds bursts of identical records, models long operations as
// steps and carries records between processes.
//
// Every line answers how serious it is (a glyph or a word prefix), who acts
// (the actor), with what tool (the executor) and what happened (the message).
// The package is synchronous: a record is written before Handle returns, and
// nothing is queued or dropped by the package itself.
package prettylog

import (
	"log/slog"
	"time"
)

// Levels. Notice sits between Info and Warn; outcomes are not a level but a
// record attribute (see Outcome).
const (
	LevelDebug  = slog.LevelDebug
	LevelInfo   = slog.LevelInfo
	LevelNotice = slog.Level(2)
	LevelWarn   = slog.LevelWarn
	LevelError  = slog.LevelError
)

// OutcomeKind is the final result of a command or step.
type OutcomeKind string

// Outcome kinds. Unknown means the result is not proven: the effect may or
// may not have happened.
const (
	OutcomeOK      OutcomeKind = "ok"
	OutcomeFailed  OutcomeKind = "failed"
	OutcomeUnknown OutcomeKind = "unknown"
)

// Reserved attribute keys. They count only at the top level of a record, are
// never printed as fields, and are read through StructureOf.
const (
	KeyActor     = "actor"
	KeyExecutor  = "executor"
	KeyOutcome   = "outcome"
	KeyStep      = "step"
	KeyParent    = "parent"
	KeyState     = "state"
	KeyTook      = "took"
	KeyErr       = "err"
	KeyDetail    = "detail"
	KeyReason    = "reason"
	KeyRepeat    = "repeat"
	KeyUntil     = "until"
	KeySnapshot  = "snapshot"
	KeyStarted   = "started"
	KeyOmitted   = "omitted"
	KeyOverflow  = "overflow"
	KeyTruncated = "truncated"
)

// Outcome marks a record as the final outcome of a command.
func Outcome(k OutcomeKind) slog.Attr { return slog.String(KeyOutcome, string(k)) }

// Actor names the component that decides and acts.
func Actor(name string) slog.Attr { return slog.String(KeyActor, name) }

// Executor names the external tool doing the work.
func Executor(name string) slog.Attr { return slog.String(KeyExecutor, name) }

// Took records a duration, printed last and dimmed.
func Took(d time.Duration) slog.Attr { return slog.Duration(KeyTook, d) }

// Err records an error. A nil error yields an empty attribute, which slog
// ignores.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}
	return slog.String(KeyErr, err.Error())
}

type secret struct{}

func (secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// Secret hides a value: it always renders and encodes as "[redacted]".
func Secret(string) slog.LogValuer { return secret{} }

// Kind is how a record is presented: one glyph, one plain prefix and one
// colour role.
type Kind int

// Kinds.
const (
	KindDebug Kind = iota
	KindInfo
	KindNotice
	KindWarn
	KindError
	KindOK
	KindUnknown
	KindSkipped
)

// KindOf classifies a record: outcome and step state first, then the level.
func KindOf(r slog.Record) Kind { return StructureOf(r).Kind }

func kindOf(level slog.Level, outcome, state string) Kind {
	switch {
	case outcome == string(OutcomeOK) || state == string(StepOK):
		return KindOK
	case outcome == string(OutcomeFailed) || state == string(StepFailed):
		return KindError
	case outcome == string(OutcomeUnknown) || state == string(StepUnknown):
		return KindUnknown
	case state == string(StepSkipped):
		return KindSkipped
	case state == string(StepRunning):
		return KindInfo
	}
	switch {
	case level >= LevelError:
		return KindError
	case level >= LevelWarn:
		return KindWarn
	case level >= LevelNotice:
		return KindNotice
	case level >= LevelInfo:
		return KindInfo
	}
	return KindDebug
}

// Glyphs are the one-cell markers per kind, plus the fold summary marker.
type Glyphs struct{ Error, Warn, Notice, Info, OK, Unknown, Debug, Skipped, Repeat string }

// DefaultGlyphs is the glyph set every yoloctl surface shares.
var DefaultGlyphs = Glyphs{
	Error: "✗", Warn: "!", Notice: "•", Info: "›", OK: "✓",
	Unknown: "?", Debug: "·", Skipped: "○", Repeat: "↳",
}

// For returns the glyph of a kind.
func (g Glyphs) For(k Kind) string {
	switch k {
	case KindError:
		return g.Error
	case KindWarn:
		return g.Warn
	case KindNotice:
		return g.Notice
	case KindInfo:
		return g.Info
	case KindOK:
		return g.OK
	case KindUnknown:
		return g.Unknown
	case KindSkipped:
		return g.Skipped
	}
	return g.Debug
}

// PlainPrefix is the word prefix a kind takes in plain output.
func PlainPrefix(k Kind) string {
	switch k {
	case KindError:
		return "error: "
	case KindWarn:
		return "warning: "
	case KindNotice:
		return "notice: "
	case KindUnknown:
		return "uncertain: "
	case KindDebug:
		return "debug: "
	}
	return ""
}
