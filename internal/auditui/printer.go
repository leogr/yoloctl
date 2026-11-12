package auditui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

type printSink struct {
	out      io.Writer
	renderer *eventRenderer
}

func (p *printSink) Emit(e prettylog.Emission) error {
	var text string
	if e.Kind == prettylog.EmissionRecord {
		text = p.renderer.text(eventFromRecord(e.Record))
	} else {
		// Typed metadata comes from the sole fold engine. No formatted-byte
		// parsing and no repeat-event backlog is needed by the line view.
		text = "    " + strings.ReplaceAll(ansi.Wrap(p.renderer.summary(e.Repeat, e.Until), max(1, p.renderer.options.Width-4), ""), "\n", "\n    ")
	}
	_, err := fmt.Fprintln(p.out, text)
	return err
}

// LogPrinter presents a finite or following event stream. First records print
// immediately; the shared prettylog engine owns consecutive folding and its
// timer. Close synchronously drains the final summary. The caller owns out;
// as with Logger, a blocking writer also blocks emission and Close.
type LogPrinter struct{ stream *eventStream }

func NewLogPrinter(ctx context.Context, out io.Writer, verbosity prettylog.Verbosity, options EventOptions) *LogPrinter {
	sink := &printSink{out: out, renderer: newEventRenderer(options)}
	return &LogPrinter{stream: newEventStream(ctx, verbosity, sink, nil)}
}

func (p *LogPrinter) Add(ctx context.Context, event audit.Event) error {
	return p.stream.add(ctx, event)
}

func (p *LogPrinter) Close() error { return p.stream.close() }
