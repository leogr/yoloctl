package main

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// addLogFlags registers the global log level, quiet and colour flags and
// installs the process Logger before any command runs. -v stays Cobra's
// --version shorthand.
func addLogFlags(root *cobra.Command) {
	f := root.PersistentFlags()
	f.String("log-level", "info", "Log output to show: "+strings.Join(logging.LogLevels, ", ")+" (debug includes raw tool output)")
	f.BoolP("quiet", "q", false, "Show only warnings, errors and final outcomes (same as --log-level warn)")
	f.String("color", "auto", `Colour log output: "auto", "always" or "never"`)
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		s, err := logSettings(cmd.Flags(), os.Getenv)
		if err != nil {
			return asUserError(err)
		}
		// A worker or background process may already have installed its
		// Logger; Init then keeps it.
		logging.Init(s)
		return nil
	}
}

// logSettings combines the environment (YOLOCTL_LOG, YOLOCTL_DEBUG) with the
// flags; flags win.
func logSettings(fs *pflag.FlagSet, getenv func(string) string) (logging.Settings, error) {
	s := logging.DefaultSettings()
	logging.FromEnv(&s, getenv)
	quiet, _ := fs.GetBool("quiet")
	if level := fs.Lookup("log-level"); level != nil && level.Changed {
		v, err := logging.ParseLogLevel(level.Value.String())
		if err != nil {
			return s, err
		}
		if quiet && v != prettylog.Quiet {
			return s, errors.New("--quiet means --log-level warn; it cannot be combined with --log-level " + level.Value.String())
		}
		s.Verbosity = v
	} else if quiet {
		s.Verbosity = prettylog.Quiet
	}
	if c := fs.Lookup("color"); c != nil && c.Changed {
		mode, err := prettylog.ParseColorMode(c.Value.String())
		if err != nil {
			return s, err
		}
		s.Color = mode
	}
	return s, nil
}

// renderCLIError shows the error a command returned when the command did not
// silence it. It is a variable so the CLI logging migration can replace the
// presentation in its own file; this default prints exactly what Cobra
// printed before.
var renderCLIError = func(root *cobra.Command, err error) {
	root.PrintErrln(root.ErrPrefix(), err.Error())
}

// executeRoot runs the command tree and renders its error once, keeping
// Cobra's rule that a command may silence its own error.
func executeRoot(root *cobra.Command, run func() (*cobra.Command, error)) error {
	root.SilenceErrors = true
	executed, err := run()
	if err != nil && (executed == nil || executed == root || !executed.SilenceErrors) {
		renderCLIError(root, err)
	}
	_ = logging.Logger().Close()
	return err
}
