package stepview

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// RecordHandler is what Failures wraps: a *prettylog.Logger, for example.
type RecordHandler interface {
	Handle(context.Context, slog.Record) error
}

// Failures is the line-output counterpart of the steps block's failure
// expansion: every record goes to the wrapped handler unchanged, and when a
// step fails its last output lines (and the count of earlier bytes not
// kept) follow as one error record, so they show at every verbosity with a
// plain or pretty line Logger. Use it where no View shows steps; at debug,
// where the raw output lines are already shown, do not wrap.
type Failures struct {
	next    RecordHandler
	tracker *prettylog.Tracker
	pending []string // failed steps restored by a snapshot, expanded at its end
}

// NewFailures wraps next.
func NewFailures(next RecordHandler) *Failures {
	return &Failures{next: next, tracker: prettylog.NewTracker(prettylog.TrackerLimits{})}
}

// Handle passes r on, then expands a failed step. It returns the first
// error of the record or of its expansion.
func (f *Failures) Handle(ctx context.Context, r slog.Record) error {
	st := prettylog.StructureOf(r)
	f.tracker.Apply(r)
	err := f.next.Handle(ctx, r)
	switch {
	case st.Snapshot && st.Step != "" && st.State == string(prettylog.StepFailed):
		f.pending = append(f.pending, st.Step) // its tail follows in the snapshot
	case st.Snapshot && st.Step != "":
	case st.Step != "" && st.State == string(prettylog.StepFailed):
		err = errors.Join(err, f.expand(ctx, st.Step))
	default: // any record after a snapshot's step records ends its replay
		for _, id := range f.pending {
			err = errors.Join(err, f.expand(ctx, id))
		}
		f.pending = nil
	}
	return err
}

// titleBytes bounds the step title in an expansion heading.
const titleBytes = 120

func cutTitle(s string) string {
	if len(s) <= titleBytes {
		return s
	}
	cut := titleBytes - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// chunkBytes keeps each expansion record within the Logger's default
// message limit, so no retained line, the last one included, is cut away.
const chunkBytes = prettylog.DefaultMessageLimit - 64

// expand writes a failed step's retained output as one or more error
// records: "Last output of <step>:" then the omitted count and the lines,
// split between records so each stays under the message limit. A single
// longer line keeps its own record (and the Logger's per-message cut).
func (f *Failures) expand(ctx context.Context, id string) error {
	for _, s := range f.tracker.Steps() {
		if s.ID != id {
			continue
		}
		if len(s.Tail) == 0 && s.Omitted == 0 {
			return nil
		}
		var lines []string
		if s.Omitted > 0 {
			lines = append(lines, "["+strconv.FormatInt(s.Omitted, 10)+" earlier bytes omitted]")
		}
		lines = append(lines, s.Tail...)
		var errs []error
		// The title is cut so the heading never takes the lines' budget.
		title := cutTitle(s.Title)
		heading := "Last output of " + title + ":"
		emit := func(body []string) {
			r := slog.NewRecord(time.Now(), prettylog.LevelError, heading+"\n"+strings.Join(body, "\n"), 0)
			errs = append(errs, f.next.Handle(ctx, r))
			heading = "Last output of " + title + " (continued):"
		}
		var chunk []string
		size := len(heading)
		for _, line := range lines {
			if len(chunk) > 0 && size+1+len(line) > chunkBytes {
				emit(chunk)
				chunk, size = nil, len(heading)
			}
			chunk = append(chunk, line)
			size += 1 + len(line)
		}
		if len(chunk) > 0 || len(lines) == 0 {
			emit(chunk)
		}
		return errors.Join(errs...)
	}
	return nil
}
