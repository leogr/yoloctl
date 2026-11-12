package auditui

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// EventOptions configures human-readable output only. JSON and export bypass
// this projection, decoration and folding completely.
type EventOptions struct {
	Color, Verbose bool
	Width          int
	Palette        prettylog.Palette // zero selects the terminal's ANSI palette
}

type eventRenderer struct {
	options EventOptions
	roles   map[prettylog.Kind]string
	bold    string
	dim     string
}

func newEventRenderer(o EventOptions) *eventRenderer {
	if o.Width <= 0 {
		o.Width = 100
	}
	if o.Palette == (prettylog.Palette{}) {
		o.Palette = prettylog.ANSIPalette
	}
	r := &eventRenderer{options: o, roles: make(map[prettylog.Kind]string)}
	if !o.Color {
		return r
	}
	for _, level := range []slog.Level{prettylog.LevelError, prettylog.LevelWarn, prettylog.LevelNotice, prettylog.LevelInfo, prettylog.LevelDebug} {
		record := slog.NewRecord(time.Time{}, level, "", 0)
		kind := prettylog.KindOf(record)
		palette := o.Palette
		if kind == prettylog.KindInfo {
			palette.Info = palette.Dim
		}
		r.roles[kind] = rolePrefix(record, palette)
	}
	// The shared renderer owns colour encoding, faint and bold. Render an
	// empty, known record to obtain its glyph's style; no child bytes, private
	// Color representation or formatted emission stream is parsed here.
	p := o.Palette
	p.Warn = p.Text
	r.bold = rolePrefix(slog.NewRecord(time.Time{}, prettylog.LevelWarn, "", 0), p)
	p.Notice = p.Dim
	r.dim = rolePrefix(slog.NewRecord(time.Time{}, prettylog.LevelNotice, "", 0), p)
	return r
}

func rolePrefix(record slog.Record, palette prettylog.Palette) string {
	var b bytes.Buffer
	l := prettylog.New(&b, prettylog.Options{Color: true, Palette: palette, Verbosity: prettylog.Trace})
	_ = l.Handle(context.Background(), record)
	_ = l.Close()
	prefix, _, _ := strings.Cut(b.String(), prettylog.DefaultGlyphs.For(prettylog.KindOf(record)))
	return prefix
}

func decorate(text, prefix string) string {
	if prefix == "" || text == "" {
		return text
	}
	return prefix + text + "\x1b[0m"
}

func (r *eventRenderer) heading(e audit.Event) string {
	kind := prettylog.KindOf(eventRecord(e))
	severity := prettylog.DefaultGlyphs.For(kind) + " " + fmt.Sprintf("%-9s", audit.Clean(e.Severity))
	return fmt.Sprintf("%s  %s  %-14s  %s", e.Time.UTC().Format("15:04:05"),
		decorate(severity, r.roles[kind]), eventOrigin(e), decorate(audit.Clean(e.Rule), r.bold))
}

func (r *eventRenderer) message(e audit.Event) string {
	return audit.FormatPrettyOutput(e, func(key, value string) string {
		return decorate(key+"=", r.dim) + decorate(value, r.bold)
	})
}

func (r *eventRenderer) fields(e audit.Event) []string {
	fields := audit.SelectedFields(e)
	var keys []string
	longest := 0
	for key, value := range fields {
		if value != "" && value != "<NA>" {
			keys = append(keys, key)
			longest = max(longest, len(key))
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, decorate(key+"=", r.dim)+strings.Repeat(" ", longest-len(key)+1)+decorate(strconv.Quote(fields[key]), r.bold))
	}
	return lines
}

func (r *eventRenderer) summary(repeat int, until time.Time) string {
	word := "times"
	if repeat == 1 {
		word = "time"
	}
	return decorate(fmt.Sprintf("%s repeated %d more %s until %s", prettylog.DefaultGlyphs.Repeat, repeat, word, until.UTC().Format("15:04:05")), r.dim)
}

func (r *eventRenderer) text(e audit.Event) string {
	heading := strings.Split(ansi.Wrap(r.heading(e), max(1, r.options.Width-4), ""), "\n")
	lines := []string{heading[0]}
	for _, continuation := range heading[1:] {
		lines = append(lines, "    "+continuation)
	}
	appendIndented := func(text, indent string) {
		for _, line := range strings.Split(ansi.Wrap(text, max(1, r.options.Width-len(indent)), ""), "\n") {
			lines = append(lines, indent+line)
		}
	}
	if message := r.message(e); message != "" {
		appendIndented(message, "    ")
	}
	if r.options.Verbose {
		for _, field := range r.fields(e) {
			appendIndented(field, "        ")
		}
	}
	return strings.Join(lines, "\n")
}

func EventText(e audit.Event, options EventOptions) string { return newEventRenderer(options).text(e) }

func eventLine(e audit.Event, color bool) string {
	return newEventRenderer(EventOptions{Color: color}).heading(e)
}
