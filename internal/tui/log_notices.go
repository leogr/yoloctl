package tui

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/logging/logtheme"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Interaction feedback is not a command log: even an error-only command log
// level must explain why a key, paste or pane action was refused. Presentation
// still uses the shared record classification, bounded escaping and vocabulary.
func noticeRecord(kind prettylog.Kind, text string) slog.Record {
	level := prettylog.LevelNotice
	switch kind {
	case prettylog.KindDebug:
		level = prettylog.LevelDebug
	case prettylog.KindInfo:
		level = prettylog.LevelInfo
	case prettylog.KindWarn, prettylog.KindUnknown:
		level = prettylog.LevelWarn
	case prettylog.KindError:
		level = prettylog.LevelError
	}
	r := slog.NewRecord(time.Time{}, level, strings.ReplaceAll(text, "\n", " "), 0)
	switch kind {
	case prettylog.KindOK:
		r.AddAttrs(prettylog.Outcome(prettylog.OutcomeOK))
	case prettylog.KindUnknown:
		r.AddAttrs(prettylog.Outcome(prettylog.OutcomeUnknown))
	case prettylog.KindSkipped:
		r.AddAttrs(slog.String(prettylog.KeyState, string(prettylog.StepSkipped)))
	}
	return r
}

func (m *model) noticeLine(kind prettylog.Kind, text string) string {
	return m.noticeLineOptions(kind, text, logging.CurrentSettings())
}

func (m *model) noticeLineOptions(kind prettylog.Kind, text string, s logging.Settings) string {
	var out strings.Builder
	_, colored := prettylog.DecideColor(os.Stderr, s.Color, os.Getenv, logging.IsTerminal)
	palette := m.noticePalette()
	l := prettylog.New(&out, prettylog.Options{
		Format: prettylog.FormatPretty, Color: colored, Palette: palette, Verbosity: prettylog.Trace,
	})
	_ = l.Handle(context.Background(), noticeRecord(kind, text))
	_ = l.Close()
	return strings.TrimSuffix(out.String(), "\n")
}

func (m *model) noticePalette() prettylog.Palette {
	if !m.bgDetected {
		return prettylog.ANSIPalette
	}
	return logtheme.Palette(m.bgIsDark, m.background)
}

func (m *model) setStatus(kind prettylog.Kind, text string) {
	m.status, m.statusKind = text, kind
}

func (m *model) statusLine() string {
	if m.status == "" {
		return ""
	}
	return m.noticeLine(m.statusKind, m.status)
}

// Popup wrapping and hit geometry operate on plain text. Apply the shared tone
// after wrapping, without adding a second glyph or changing any cell widths.
func (m *model) noticeFooter(kind prettylog.Kind) func(string, int) string {
	return func(line string, _ int) string {
		s := logging.CurrentSettings()
		_, colored := prettylog.DecideColor(os.Stderr, s.Color, os.Getenv, logging.IsTerminal)
		if !colored {
			return line
		}
		theme := spaceview.NewTheme(m.bgIsDark || !m.bgDetected, m.background, nil)
		return spaceview.StyleText(line, spaceview.Style{
			FG: theme.LogTone(kind), Bold: kind == prettylog.KindWarn || kind == prettylog.KindError || kind == prettylog.KindUnknown,
		})
	}
}
