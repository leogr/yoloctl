package main

import (
	"os"

	"golang.org/x/term"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/leogr/yoloctl/pkg/prettylog/stepview"
)

// installStepView puts the interactive steps block on the process Logger
// when stderr is a terminal. It stays idle (draws nothing) until a step
// runs. warn and error show only problems and outcomes, and debug asks for
// the raw tool output as lines, so only the default info level gets a block. Line-only output never
// queries the terminal: the block uses the terminal's own 16 colours.
//
// A TUI pane passes a private step click channel; it is always claimed here
// (so no subprocess inherits it) and feeds the block's Click, or is closed
// when there is no block.
func installStepView(s logging.Settings) {
	clicks, _ := termio.TakeInheritedStepClicks()
	if workerProcess || stepview.Current() != nil || !logging.IsTerminal(os.Stderr) || s.Verbosity != prettylog.Normal {
		if clicks != nil {
			_ = clicks.Close()
		}
		return
	}
	_, color := prettylog.DecideColor(os.Stderr, s.Color, os.Getenv, logging.IsTerminal)
	v := stepview.Install(logging.Logger(), os.Stderr, stepview.Options{
		Color: color, Palette: prettylog.ANSIPalette, ActorWidth: 20, Verbosity: s.Verbosity,
		Size: func() (int, int) {
			w, h, err := term.GetSize(int(os.Stderr.Fd()))
			if err != nil {
				return 0, 0
			}
			return w, h
		},
		Pointer: hostLogPointer,
	})
	if clicks != nil {
		go func() {
			defer func() { _ = clicks.Close() }()
			_ = termio.ReadStepClicks(clicks, v.Click)
		}()
	}
}

// hostLogPointer names where a host step's output is kept in full: output
// lines under a host step are Lima launcher output (lima.WithProgress), and
// each call's last 64 KiB is kept in host.log under its space.
func hostLogPointer(actor string, root prettylog.StepView) string {
	if actor != logging.Host {
		return ""
	}
	for _, a := range root.Attrs {
		if a.Key == logging.KeySpace && layout.ValidateExistingSpaceName(a.Value.String()) == nil {
			return "full output: yoloctl space logs " + a.Value.String() + " --host"
		}
	}
	return ""
}

// workerProcess is set in a controller worker, whose Logger writes wire
// records for the host: the worker never renders a steps block.
var workerProcess bool

// closeStepView leaves the block's steps in the scrollback before the
// process Logger closes.
func closeStepView() { stepview.Current().Close() }
