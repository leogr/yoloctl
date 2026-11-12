package tui

import (
	"image/color"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/logging/logtheme"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func withNoticeSettings(t *testing.T, level prettylog.Verbosity) {
	t.Helper()
	restore := logging.SetLogger(prettylog.New(io.Discard, prettylog.Options{}))
	logging.InitWorker(io.Discard, level)
	t.Cleanup(func() { _ = logging.Logger().Close(); restore() })
}

func TestErrorOnlyStillExplainsDirectPaneRefusal(t *testing.T) {
	withNoticeSettings(t, prettylog.Errors)
	m := newTestModel(t, 100, 30)
	m.paneNotice("one pane: nothing to swap")
	shown := strings.Join(m.noticeOverlay().frame.render(60, 5, ""), "\n")
	if !strings.Contains(shown, "! one pane: nothing to swap") {
		t.Fatalf("refusal hidden at error level: %q", shown)
	}
	m.setStatus(prettylog.KindUnknown, "outcome remains uncertain")
	if got := m.inputLine(); !strings.Contains(got, "? outcome remains uncertain") {
		t.Fatalf("unknown status misclassified: %q", got)
	}
}

func TestNoticeUsesSharedKindAndEscapesControls(t *testing.T) {
	withNoticeSettings(t, prettylog.Errors)
	m := newTestModel(t, 100, 30)
	for _, kind := range []prettylog.Kind{prettylog.KindDebug, prettylog.KindInfo, prettylog.KindNotice, prettylog.KindWarn, prettylog.KindError, prettylog.KindOK, prettylog.KindUnknown, prettylog.KindSkipped} {
		r := noticeRecord(kind, "text")
		if got := prettylog.KindOf(r); got != kind {
			t.Fatalf("kind %v became %v", kind, got)
		}
		line := m.noticeLine(kind, "text\x1b]52;c;eA==\a\nsecond\r\tend")
		if !strings.HasPrefix(line, prettylog.DefaultGlyphs.For(kind)+" ") || strings.ContainsAny(line, "\x1b\a\n\r\t") || !strings.Contains(line, `\x1b`) {
			t.Fatalf("unsafe/misclassified line %q", line)
		}
	}
	line := m.noticeLine(prettylog.KindWarn, strings.Repeat("界", 2000))
	if len(line) > 1100 || !strings.Contains(line, "bytes)") {
		t.Fatalf("unbounded notice: %d", len(line))
	}
}

func TestNoticeBackgroundUsesSharedThemeWithoutAQuery(t *testing.T) {
	withNoticeSettings(t, prettylog.Normal)
	m := newTestModel(t, 100, 30)
	if got := m.noticePalette(); got != prettylog.ANSIPalette {
		t.Fatal("unknown background invented a light/dark log palette")
	}
	var lines []string
	for _, bg := range []color.Color{color.Black, color.White} {
		_, cmd := m.Update(tea.BackgroundColorMsg{Color: bg})
		if cmd != nil {
			t.Fatal("background report caused another query")
		}
		want := logtheme.Palette(m.bgIsDark, bg)
		var out strings.Builder
		logger := prettylog.New(&out, prettylog.Options{Format: prettylog.FormatPretty, Color: true, Palette: want, Verbosity: prettylog.Trace})
		_ = logger.Handle(t.Context(), noticeRecord(prettylog.KindWarn, "careful"))
		_ = logger.Close()
		line := m.noticeLineOptions(prettylog.KindWarn, "careful", logging.Settings{Color: prettylog.ColorAlways})
		if line != strings.TrimSuffix(out.String(), "\n") {
			t.Fatalf("theme drift: %q vs %q", line, out.String())
		}
		if ansi.Strip(line) != "! careful" {
			t.Fatalf("theme changed text: %q", line)
		}
		lines = append(lines, line)
	}
	if lines[0] == lines[1] {
		t.Fatal("light/dark warning tone did not change")
	}
}
