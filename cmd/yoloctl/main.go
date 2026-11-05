package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/buildinfo"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/tui"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var version = buildinfo.Version

// classifyError preserves child statuses, maps tagged user errors to 1,
// a signal-detached attach client to 75 and other errors (including untagged
// Cobra errors) to 2.
const (
	exitOK      = 0
	exitUser    = 1
	exitRuntime = 2
	// exitDetached (EX_TEMPFAIL) reports an attach client that a signal ended
	// before its setup handed off: inspect the box session, then reattach.
	exitDetached = 75
)

func main() {
	// This probe runs as the ordinary VM user before a dynamic Linux build is
	// installed. Do not initialize configuration, Lima, or a workload here.
	if len(os.Args) == 2 && os.Args[1] == session.BinaryCheckArgument {
		fmt.Println(session.BinaryCheckResponse)
		return
	}
	// A privileged box's PID 1. It owns its signals and never initializes CLI
	// configuration, controller context, the Lima home or an operator client.
	if len(os.Args) > 1 && os.Args[1] == boxendpoint.Command {
		os.Exit(boxendpoint.Main(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == openshell.NetworkWorkerCommand {
		os.Exit(openshell.NetworkWorkerMain(os.Args[2:]))
	}
	ctx, stop := commandSignalContext()
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == port.ForwardWorkerCommand {
		err := runOpenShellForwardInternal(ctx, os.Args[2:])
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "OpenShell forward:", err)
		}
		os.Exit(classifyError(err))
	}
	if len(os.Args) > 1 && os.Args[1] == "vm-internal" {
		err := runVMInternal(ctx, os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "VM:", err)
		}
		os.Exit(classifyError(err))
	}
	if len(os.Args) > 1 && os.Args[1] == "controller-internal" {
		err := runControllerInternal(ctx, os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(classifyError(err))
	}
	if os.Getenv(spaceenv.ContextEnv) != "" {
		// Only infrastructure children inherit this variable. Loading it still
		// requires a fixed root-owned context and verified native filesystem.
		if err := controller.LoadRuntime(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "yoloctl:", err)
			os.Exit(exitRuntime)
		}
	}

	// Set the legacy Lima default before external-tool queries. Targeted
	// VM/disk commands override their child's LIMA_HOME from the bootstrap,
	// so old and new macOS homes coexist without changing process state.
	// Never silently fall back to the user's unrelated ~/.lima.
	operator, err := loadOperatorClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "yoloctl:", err)
		os.Exit(exitRuntime)
	}
	if operator == nil && readOnlyStartup(os.Args[1:]) {
		home, err := layout.LimaHome()
		if err == nil {
			err = os.Setenv("LIMA_HOME", home)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "yoloctl:", err)
			os.Exit(exitRuntime)
		}
	}
	if operator == nil && !readOnlyStartup(os.Args[1:]) {
		if err := vmhost.SetupHome(); err != nil {
			fmt.Fprintln(os.Stderr, "yoloctl:", err)
			os.Exit(exitRuntime)
		}
	}
	root := newClientRootCmd()
	if spaceenv.Native() {
		// Independent holders and their SSH proxy children execute locally.
		root = newRootCmd()
	}
	err = root.ExecuteContext(ctx)
	os.Exit(classifyError(err))
}

// classifyError maps an error returned by Cobra/RunE to a process exit
// code. Sentinel-driven so call sites can keep returning ordinary
// errors; the cmd-layer wrappers (asUserError, etc.) tag the ones we
// want to surface as user-input failures.
func classifyError(err error) int {
	if err == nil {
		return exitOK
	}
	// An attached agent's own exit status propagates as yoloctl's exit
	// code (kubectl-style), so `yoloctl box create` / `box attach` behave
	// like running the agent directly.
	var agentExit *session.ExitError
	if errors.As(err, &agentExit) {
		return agentExit.ExitCode()
	}
	var detached *controller.ClientDetached
	if errors.As(err, &detached) {
		return exitDetached
	}
	var controllerExit *controller.ExitError
	if errors.As(err, &controllerExit) {
		return controllerExit.ExitCode()
	}
	var passthrough interface{ passthroughExitCode() int }
	if errors.As(err, &passthrough) {
		return passthrough.passthroughExitCode()
	}
	if IsUserError(err) {
		return exitUser
	}
	return exitRuntime
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "yoloctl",
		Short: "yoloctl — let coding agents yolo safely",
		Long: `yoloctl — let coding agents yolo safely.

Run AI coding agents inside hard-isolated spaces.
A space is a VM-level boundary; inside it, agentboxes (per-agent
containerized sandboxes) each bind an explicit host workdir (or
VM-local ephemeral) at /workdir.`,
		Version:      version,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		// Bare `yoloctl` on a terminal opens the TUI shell;
		// anything else (pipes, scripts, CI) keeps the classic help.
		// YOLOCTL_TUI guards nesting as insurance only — recursion is
		// already structurally impossible (tabs host yoloctl
		// subcommands, which never re-enter this hook).
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Getenv("YOLOCTL_TUI") != "1" &&
				term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
				return tui.Run(cmd.Context())
			}
			return cmd.Help()
		},
	}
	// Cobra's own flag-parsing failures (unknown flag, missing flag
	// argument) are user input mistakes — map them to exit 1 just like
	// our own validation errors.
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return asUserError(err)
	})
	cmd.AddCommand(
		newSpaceCmd(),
		newAgentboxCmd(),
		newImageCmd(),
		newAccountCmd(),
		// The root attach shortcut shares the box command's implementation.
		newAgentboxAttachCmd(),
		newSSHProxyCmd(),
		newSessionHoldCmd(),
		newVersionCmd(),
		newInstallCmd(),
		newPortCmd(),
		newAgentCmd(),
		newFeatureCmd(),
		newCapabilityCmd(),
	)
	registerCompletions(cmd)
	rejectUnknownCommandGroups(cmd)
	registerSpaceOperations(cmd)
	return cmd
}

// Cobra treats an unknown child of a command group as arguments to that group.
// Groups without Run/RunE then print help and succeed, which makes a removed or
// misspelled command look successful to scripts. Preserve bare-group help but
// reject unexpected children.
func rejectUnknownCommandGroups(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		rejectUnknownCommandGroups(child)
	}
	if len(cmd.Commands()) == 0 || cmd.Run != nil || cmd.RunE != nil {
		return
	}
	cmd.RunE = func(group *cobra.Command, args []string) error {
		if len(args) == 0 {
			return group.Help()
		}
		return asUserError(fmt.Errorf("unknown command %q for %q", args[0], group.CommandPath()))
	}
}

// readOnlyStartup keeps introspection and installer preflight free of root
// creation. Infrastructure/native dispatch and operator loading still run first.
func readOnlyStartup(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return args[0] == "version" || args[0] == "install"
}
