// Package logging binds the prettylog renderer to yoloctl: the actor and
// executor vocabulary, the verbosity and colour settings, and the process
// Logger that commands, libraries and background processes share.
//
// It imports no UI package, so helpers, libraries and the guest init binary
// may use it. The terminal theme is applied separately by logtheme.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Actors: the yoloctl component that decides and acts.
const (
	Host       = "host"
	VM         = "vm"
	Controller = "controller"
	Builder    = "builder"
	Box        = "box"
	Session    = "session"
	Storage    = "storage"
	Audit      = "audit"
	Chat       = "chat"
	Install    = "install"
)

// Executors: the external tool doing the work.
const (
	BuildKit    = "buildkit"
	Nerdctl     = "nerdctl"
	Containerd  = "containerd"
	Limactl     = "limactl"
	Firecracker = "firecracker"
	OpenShell   = "openshell"
	Falco       = "falco"
	SSH         = "ssh"
)

// Field keys shared by yoloctl records.
const (
	KeySpace   = "space"
	KeyBox     = "box"
	KeyAccount = "account"
	KeyImage   = "image"
	KeyPath    = "path"
)

// Presentation defaults shared by every yoloctl Logger.
const (
	actorWidth = 20
	foldWindow = 10 * time.Second
)

var defaultFields = []string{KeySpace, KeyBox, KeyImage}

// Settings are what the user asked for with --log-level, -q and --color or
// the environment.
type Settings struct {
	Verbosity prettylog.Verbosity
	Color     prettylog.ColorMode
}

// DefaultSettings is Normal verbosity with automatic colour.
func DefaultSettings() Settings {
	return Settings{Verbosity: prettylog.Normal, Color: prettylog.ColorAuto}
}

// LogLevels are the --log-level values, from most to least output.
var LogLevels = []string{"debug", "info", "warn", "error"}

// ParseLogLevel maps a --log-level value to a verbosity: debug shows debug
// records and raw tool output, info is the default, warn shows warnings,
// errors, uncertain results and final outcomes (like -q), and error shows only
// errors and uncertain results.
func ParseLogLevel(s string) (prettylog.Verbosity, error) {
	switch s {
	case "debug":
		return prettylog.Trace, nil
	case "info":
		return prettylog.Normal, nil
	case "warn":
		return prettylog.Quiet, nil
	case "error":
		return prettylog.Errors, nil
	}
	return prettylog.Normal, fmt.Errorf("log level must be one of %s", strings.Join(LogLevels, ", "))
}

// FromEnv applies YOLOCTL_LOG (a --log-level value) and the older
// YOLOCTL_DEBUG=1 (debug). An explicit YOLOCTL_LOG wins; an unknown value is
// ignored.
func FromEnv(s *Settings, getenv func(string) string) {
	if getenv("YOLOCTL_DEBUG") == "1" {
		s.Verbosity = prettylog.Trace
	}
	if v, err := ParseLogLevel(getenv("YOLOCTL_LOG")); err == nil {
		s.Verbosity = v
	}
}

var (
	mu        sync.Mutex
	current   *prettylog.Logger
	installed bool
	settings  = DefaultSettings()
	palette   = prettylog.ANSIPalette
)

// Init installs the host CLI's process Logger on stderr: format and colour
// decided once for stderr, the shared palette and 10 s folding. It also
// becomes slog's default. Once a process Logger is installed (by Init,
// InitWorker, InitFile or SetLogger), later Init calls return it unchanged.
func Init(s Settings) *prettylog.Logger {
	mu.Lock()
	defer mu.Unlock()
	if installed {
		return current
	}
	format, color := prettylog.DecideColor(os.Stderr, s.Color, os.Getenv, IsTerminal)
	l := prettylog.New(os.Stderr, prettylog.Options{
		Format: format, Color: color, Palette: palette, Verbosity: s.Verbosity,
		ActorWidth: actorWidth, Fields: defaultFields, Fold: prettylog.FoldOptions{Window: foldWindow},
	})
	installLocked(l, s)
	return l
}

// InitWorker installs a worker's process Logger: framed records on the log
// pipe, filtered with the verbosity the host sent.
func InitWorker(w io.Writer, v prettylog.Verbosity) *prettylog.Logger {
	l := prettylog.New(w, prettylog.Options{Format: prettylog.FormatWire, Verbosity: v})
	mu.Lock()
	defer mu.Unlock()
	installLocked(l, Settings{Verbosity: v, Color: prettylog.ColorNever})
	return l
}

// InitFile installs a background process's Logger on a log file writer.
func InitFile(w io.Writer) *prettylog.Logger {
	l := NewFileLogger(w)
	mu.Lock()
	defer mu.Unlock()
	installLocked(l, Settings{Verbosity: prettylog.Trace, Color: prettylog.ColorNever})
	return l
}

func installLocked(l *prettylog.Logger, s Settings) {
	current, settings, installed = l, s, true
	slog.SetDefault(l.Slog())
}

// Logger is the process Logger. Before any Init it is a plain, uncoloured
// Logger on stderr at Normal verbosity, which is not installed as slog's
// default.
func Logger() *prettylog.Logger {
	mu.Lock()
	defer mu.Unlock()
	if current == nil {
		current = prettylog.New(os.Stderr, prettylog.Options{
			Format: prettylog.FormatPlain, Verbosity: prettylog.Normal, Fields: defaultFields,
		})
	}
	return current
}

// L is the process Logger's slog front end.
func L() *slog.Logger { return Logger().Slog() }

// Verbosity is the verbosity the process Logger was installed with (Normal
// before Init). The host sends it to the worker.
func Verbosity() prettylog.Verbosity {
	mu.Lock()
	defer mu.Unlock()
	return settings.Verbosity
}

// CurrentSettings is the Settings the process Logger was installed with
// (DefaultSettings before Init), for views that build their own Logger on
// another writer with NewFollowLogger.
func CurrentSettings() Settings {
	mu.Lock()
	defer mu.Unlock()
	return settings
}

// SetLogger installs l as the process Logger and slog default, for tests and
// fixtures that capture diagnostics. restore puts both back as they were.
func SetLogger(l *prettylog.Logger) (restore func()) {
	mu.Lock()
	prev, prevSettings, prevInstalled := current, settings, installed
	prevDefault := slog.Default()
	current, installed = l, true
	slog.SetDefault(l.Slog())
	mu.Unlock()
	return func() {
		mu.Lock()
		defer mu.Unlock()
		current, settings, installed = prev, prevSettings, prevInstalled
		slog.SetDefault(prevDefault)
	}
}

// NewFileLogger is a Logger for a background log file: plain, UTC RFC 3339
// timestamps, never coloured, every record, 10 s folding.
func NewFileLogger(w io.Writer) *prettylog.Logger {
	return prettylog.New(w, prettylog.Options{
		Format: prettylog.FormatPlain, Verbosity: prettylog.Trace, Time: prettylog.TimeRFC3339,
		Fold: prettylog.FoldOptions{Window: foldWindow},
	})
}

// NewFollowLogger is a Logger for a follow view (audit or space logs) on w:
// local clock timestamps, the shared palette and 10 s folding with key.
func NewFollowLogger(w io.Writer, s Settings, key func(slog.Record) (string, bool)) *prettylog.Logger {
	format, color := prettylog.DecideColor(w, s.Color, os.Getenv, IsTerminal)
	mu.Lock()
	p := palette
	mu.Unlock()
	return prettylog.New(w, prettylog.Options{
		Format: format, Color: color, Palette: p, Verbosity: s.Verbosity, Time: prettylog.TimeClock,
		ActorWidth: actorWidth, Fields: defaultFields, Fold: prettylog.FoldOptions{Window: foldWindow, Key: key},
	})
}

// SetPalette replaces the shared palette: the process Logger uses it from
// its next line, and Loggers made afterwards start with it. logtheme calls it
// when an input owner reports the terminal background.
func SetPalette(p prettylog.Palette) {
	mu.Lock()
	palette = p
	l := current
	mu.Unlock()
	if l != nil {
		l.SetPalette(p)
	}
}

// IsTerminal reports whether w is a terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(f.Fd()))
}
