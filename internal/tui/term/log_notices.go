package term

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// SetLogPalette updates future host-owned terminal notices; PTY bytes and their
// application-owned palette are unaffected. Pumps copy the palette under mu.
func (e *emulator) SetLogPalette(p prettylog.Palette) {
	e.mu.Lock()
	e.logPalette = p
	e.mu.Unlock()
}

func (e *emulator) logNotice(code int, text string) string {
	return e.logNoticeOptions(code, text, logging.CurrentSettings())
}

func (e *emulator) logNoticeOptions(code int, text string, s logging.Settings) string {
	e.mu.Lock()
	p := e.logPalette
	e.mu.Unlock()
	if p == (prettylog.Palette{}) {
		p = prettylog.ANSIPalette
	}
	_, color := prettylog.DecideColor(os.Stderr, s.Color, os.Getenv, logging.IsTerminal)
	var out strings.Builder
	l := prettylog.New(&out, prettylog.Options{
		Format: prettylog.FormatPretty, Color: color, Palette: p, Verbosity: prettylog.Trace,
	})
	r := slog.NewRecord(time.Time{}, prettylog.LevelInfo, text, 0)
	result := prettylog.OutcomeOK
	if code != 0 {
		result = prettylog.OutcomeFailed
	}
	r.AddAttrs(prettylog.Outcome(result))
	_ = l.Handle(context.Background(), r)
	_ = l.Close()
	return strings.TrimSuffix(out.String(), "\n")
}
