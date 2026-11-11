package main

import (
	"os"

	"golang.org/x/term"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/leogr/yoloctl/pkg/prettylog/stepview"
)

// installStepView puts the interactive steps block on the process Logger
// when stderr is a terminal. It stays idle (draws nothing) until a step
// runs. warn and error show only problems and outcomes, and debug asks for
// the raw tool output as lines, so only the default info level gets a block. Line-only output never
// queries the terminal: the block uses the terminal's own 16 colours.
func installStepView(s logging.Settings) {
	if workerProcess || stepview.Current() != nil || !logging.IsTerminal(os.Stderr) {
		return
	}
	if s.Verbosity != prettylog.Normal {
		return
	}
	_, color := prettylog.DecideColor(os.Stderr, s.Color, os.Getenv, logging.IsTerminal)
	stepview.Install(logging.Logger(), os.Stderr, stepview.Options{
		Color: color, Palette: prettylog.ANSIPalette, ActorWidth: 20, Verbosity: s.Verbosity,
		Size: func() (int, int) {
			w, h, err := term.GetSize(int(os.Stderr.Fd()))
			if err != nil {
				return 0, 0
			}
			return w, h
		},
	})
}

// workerProcess is set in a controller worker, whose Logger writes wire
// records for the host: the worker never renders a steps block.
var workerProcess bool

// closeStepView leaves the block's steps in the scrollback before the
// process Logger closes.
func closeStepView() { stepview.Current().Close() }
