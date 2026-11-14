package main

import (
	"context"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/spf13/cobra"
)

type startModeWarnedKey struct{}

// warnStartModeOnce prints a directive-56 warning at most once per command.
func warnStartModeOnce(ctx context.Context, message string) {
	if warned, ok := ctx.Value(startModeWarnedKey{}).(*bool); ok {
		if *warned {
			return
		}
		*warned = true
	}
	cliWarn(logging.Box, "%s", message)
}

// startModeWarningExcluded are the box commands that do not print the
// warning: recreate and destroy remove the old container; list, create and
// copy do not work on one existing box.
var startModeWarningExcluded = map[string]bool{"box recreate": true, "box destroy": true, "box list": true, "box ls": true, "box create": true, "box copy": true}

// registerStartModeWarnings makes every command that works on one named box
// print, once and whatever its result, the warning of a box created before
// the session launcher or a privileged box running an older endpoint
// (directive 56).
// The read is optional: when it fails nothing is printed and nothing changes.
func registerStartModeWarnings(root *cobra.Command) {
	for _, cmd := range root.Commands() {
		registerStartModeWarnings(cmd)
	}
	if root.RunE == nil {
		return
	}
	original := root.RunE
	root.RunE = func(cmd *cobra.Command, args []string) error {
		path := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
		oneBox := strings.HasPrefix(path, "box ") || path == "attach"
		if !startModeWarningsActive() || startModeWarningExcluded[path] || !oneBox || len(args) == 0 || validateAgentboxName(args[0]) != nil {
			return original(cmd, args)
		}
		warned := false
		cmd.SetContext(context.WithValue(cmd.Context(), startModeWarnedKey{}, &warned))
		err := original(cmd, args)
		if !warned {
			if message := startModeWarningForFn(cmd, args[0]); message != "" {
				warnStartModeOnce(cmd.Context(), message)
			}
		}
		return err
	}
}

// startModeWarningsActive: only a native worker can read a box's record;
// tests replace it.
var startModeWarningsActive = spaceenv.Native

// startModeWarningForFn reads a box's warning; tests replace it.
var startModeWarningForFn = startModeWarningFor

// startModeWarningFor reads how the box takes a session; "" when it needs
// nothing or cannot be read.
func startModeWarningFor(cmd *cobra.Command, name string) string {
	space, err := selectedSpace(cmd)
	if err != nil {
		return ""
	}
	c := container.New(space)
	d, err := c.Inspect(cmd.Context(), name)
	if err != nil {
		return ""
	}
	support, err := boxLauncherFn(cmd.Context(), c, name, d)
	switch {
	case err != nil:
		return ""
	case support.NeedsRecreate:
		return agentbox.RecreateWarning(name)
	case support.NeedsRestart:
		return agentbox.RestartWarning(name)
	}
	return ""
}
