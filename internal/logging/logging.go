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

// Places: where an actor runs, shown before it as place▸actor.
const (
	PlaceHost  = "host"
	PlaceSpace = "space"
)

// Presentation defaults shared by every yoloctl Logger. LabelWidth pads the
// label column: the longest place▸actor is space▸controller (16 columns),
// padded to 18 as in the approved layout.
const (
	LabelWidth = 18
	foldWindow = 10 * time.Second
)

// Label maps a record's actor to the place it runs in and the name shown
// after it. The host CLI itself shows as host▸yoloctl. An actor outside the
// vocabulary keeps its plain label.
func Label(actor string) (place, name string) {
	switch actor {
	case Host:
		return PlaceHost, "yoloctl"
	case VM, Install:
		return PlaceHost, actor
	case Controller, Builder, Box, Session, Storage, Audit, Chat:
		return PlaceSpace, actor
	}
	return "", actor
}

// ParseLabel reads a plain label back into its actor: "place>name" (this
// release, the tool is then the message's "tool: " prefix) or
// "actor[/executor]" (earlier releases). ok is false for anything else.
func ParseLabel(head string) (actor, executor string, toolInMessage, ok bool) {
	if place, name, cut := strings.Cut(head, ">"); cut {
		if place != PlaceHost && place != PlaceSpace || name == "" {
			return "", "", false, false
		}
		actor = name
		if place == PlaceHost && name == "yoloctl" {
			actor = Host
		}
		p, n := Label(actor)
		return actor, "", true, p == place && n == name
	}
	actor, executor, _ = strings.Cut(head, "/")
	switch actor {
	case Host, VM, Controller, Builder, Box, Session, Storage, Audit, Chat, Install:
		return actor, executor, false, true
	}
	return "", "", false, false
}

// fileTools are the executors yoloctl's own log files name: the vocabulary
// above plus the VM and box log sources the host renders.
var fileTools = map[string]bool{
	BuildKit: true, Nerdctl: true, Containerd: true, Limactl: true, Firecracker: true,
	OpenShell: true, Falco: true, SSH: true, "kernel": true, "lima": true, "ocsf": true,
}

// CutTool splits a "tool: message" prefix written after a place>name label.
// Only a known tool counts; any other first word is message text.
func CutTool(text string) (tool, rest string, ok bool) {
	head, rest, cut := strings.Cut(text, ": ")
	if !cut || !fileTools[head] {
		return "", text, false
	}
	return head, rest, true
}

// Presentation adds the shared label hook and label width to o. Every
// Logger that prints actor labels is built from it.
func Presentation(o prettylog.Options) prettylog.Options {
	o.Label, o.ActorWidth = Label, LabelWidth
	return o
}

// named records the spaces the current outer command has named with its
// "using space" line. It is per command invocation, not per Logger, so live
// redraws and buffered step lines see the same answer.
var named struct {
	sync.Mutex
	spaces map[string]bool
}

// BeginCommand starts an outer command invocation: no space is named yet.
// Nested operations of the same command share its state.
func BeginCommand() {
	named.Lock()
	named.spaces = nil
	named.Unlock()
}

// NameSpace records that the current command named space and reports
// whether it was not named before.
func NameSpace(space string) bool {
	named.Lock()
	defer named.Unlock()
	if named.spaces[space] {
		return false
	}
	if named.spaces == nil {
		named.spaces = map[string]bool{}
	}
	named.spaces[space] = true
	return true
}

func spaceNamed(space string) bool {
	named.Lock()
	defer named.Unlock()
	return named.spaces[space]
}

// HideNamedSpace hides a space field that the command already named, only
// at the normal (info) level where that line is shown. Terminal stderr and
// its steps block use it; plain output and other levels keep the field.
func HideNamedSpace(key, value string) bool {
	return key == KeySpace && Verbosity() == prettylog.Normal && spaceNamed(value)
}

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
	o := Presentation(prettylog.Options{
		Format: format, Color: color, Palette: palette, Verbosity: s.Verbosity,
		TerminalWidth: terminalWidth(os.Stderr), GroupStepCompletions: true,
		Fields: defaultFields, Fold: prettylog.FoldOptions{Window: foldWindow},
	})
	if format == prettylog.FormatPretty {
		o.HideField = HideNamedSpace
	}
	l := prettylog.New(os.Stderr, o)
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
		current = prettylog.New(os.Stderr, Presentation(prettylog.Options{
			Format: prettylog.FormatPlain, Verbosity: prettylog.Normal, Fields: defaultFields,
		}))
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
	return prettylog.New(w, Presentation(prettylog.Options{
		Format: prettylog.FormatPlain, Verbosity: prettylog.Trace, Time: prettylog.TimeRFC3339,
		Fold: prettylog.FoldOptions{Window: foldWindow},
	}))
}

// NewFollowLogger is a Logger for a follow view (audit or space logs) on w:
// local clock timestamps, the shared palette and 10 s folding with key.
func NewFollowLogger(w io.Writer, s Settings, key func(slog.Record) (string, bool)) *prettylog.Logger {
	format, color := prettylog.DecideColor(w, s.Color, os.Getenv, IsTerminal)
	mu.Lock()
	p := palette
	mu.Unlock()
	return prettylog.New(w, Presentation(prettylog.Options{
		Format: format, Color: color, Palette: p, Verbosity: s.Verbosity, Time: prettylog.TimeClock,
		TerminalWidth: terminalWidth(w),
		Fields:        defaultFields, Fold: prettylog.FoldOptions{Window: foldWindow, Key: key},
	}))
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

// Query only terminal columns, anew for every rendered write, so resizing does
// not leave the logger using its launch width. Files and failed probes opt out.
func terminalWidth(w io.Writer) func() int {
	return func() int {
		if !IsTerminal(w) {
			return 0
		}
		f := w.(interface{ Fd() uintptr })
		width, _, err := term.GetSize(int(f.Fd()))
		if err != nil {
			return 0
		}
		return width
	}
}
