package term

import (
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestExitFeedbackUsesSharedPaletteAndIgnoresLogFilter(t *testing.T) {
	restore := logging.SetLogger(prettylog.New(io.Discard, prettylog.Options{}))
	logging.InitWorker(io.Discard, prettylog.Errors)
	t.Cleanup(func() { _ = logging.Logger().Close(); restore() })
	c := NewConsole(8, 80, 20, nil)
	t.Cleanup(func() { _ = c.Close() })
	c.SetLogPalette(prettylog.Palette{OK: prettylog.RGB(1, 2, 3), Error: prettylog.RGB(4, 5, 6)})
	if got := c.logNoticeOptions(0, "exit 0", logging.Settings{Color: prettylog.ColorAlways}); !strings.Contains(got, "38;2;1;2;3m") || !strings.Contains(ansi.Strip(got), "✓ exit 0") {
		t.Fatalf("success: %q", got)
	}
	if got := c.logNoticeOptions(2, "exit 2", logging.Settings{Color: prettylog.ColorAlways}); !strings.Contains(got, "38;2;4;5;6m") || !strings.Contains(ansi.Strip(got), "✗ exit 2") {
		t.Fatalf("failure: %q", got)
	}
	if got := c.logNoticeOptions(2, "bad\x1b[2J\a", logging.Settings{Color: prettylog.ColorAlways}); strings.Contains(got, "\x1b[2J") || strings.ContainsRune(got, '\a') {
		t.Fatalf("untrusted controls: %q", got)
	}
}
