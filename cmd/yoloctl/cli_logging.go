package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/lima"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/leogr/yoloctl/pkg/prettylog/stepview"
	"github.com/spf13/cobra"
)

// CLI messages use the process logger: the host owns presentation and a
// controller worker installs the wire handler. Data and interactive streams
// keep their existing writers and never pass through these helpers.
func cliLog(level slog.Level, actor, format string, args ...any) {
	logging.L().LogAttrs(context.Background(), level,
		strings.TrimRight(fmt.Sprintf(format, args...), "\r\n"), prettylog.Actor(actor))
}

func cliWarn(actor, format string, args ...any) { cliLog(slog.LevelWarn, actor, format, args...) }
func cliNotice(actor, format string, args ...any) {
	cliLog(prettylog.LevelNotice, actor, format, args...)
}
func cliInfo(actor, format string, args ...any) { cliLog(slog.LevelInfo, actor, format, args...) }

// C replaces A's named hook without changing main.go/root flag ownership.
func init() { renderCLIError = func(_ *cobra.Command, err error) { renderLoggedCLIError(err) } }

// renderLoggedCLIError is the one error-presentation seam called by main. A prior
// successful subcall's outcome is not evidence about this error. In particular,
// a lost reply must always retain its own uncertainty and request identifier.
func renderLoggedCLIError(err error) {
	if err == nil {
		return
	}
	var rendered *cliRenderedError
	if errors.As(err, &rendered) {
		return
	}
	if controller.OutcomeRendered(err) {
		return
	}
	if cliUncertain(err) {
		logging.Logger().Outcome(prettylog.OutcomeUnknown, "Command outcome is unknown", prettylog.Actor(logging.Controller), prettylog.Err(err))
		return
	}
	logging.Logger().Outcome(prettylog.OutcomeFailed, "Command failed", prettylog.Actor(logging.Host), prettylog.Err(err))
}

// cliRenderedError relates a successfully rendered outcome to this exact
// operation. Unwrap keeps all existing exit/status classifications intact.
type cliRenderedError struct{ error }

func (e *cliRenderedError) Unwrap() error { return e.error }

type cliStepContextKey struct{}

func cliStep(ctx context.Context, title string, attrs ...slog.Attr) *prettylog.Step {
	if parent, ok := ctx.Value(cliStepContextKey{}).(*prettylog.Step); ok {
		return parent.Step(title, attrs...)
	}
	return logging.Logger().StartStep(title, attrs...)
}

func cliUncertain(err error) bool {
	var lost *controller.OutcomeUnknown
	var interrupted *controller.ImageInterrupted
	var remote *controller.ExitError
	return errors.As(err, &lost) || errors.As(err, &interrupted) || errors.As(err, &remote) && (remote.State == "uncertain" || remote.State == "command-uncertain")
}

func cliEndStep(step *prettylog.Step, err error) {
	switch {
	case err == nil:
		step.Done()
	case cliUncertain(err):
		step.Unknown(err)
	default:
		step.Fail(err)
	}
}

// cliOperation owns one command's steps and final outcome. It changes only
// presentation: the closure still owns all guards, prompts, effects and errors.
func cliOperation(cmd *cobra.Command, title, success, space string, run func() error) (result error) {
	ctx := cmd.Context()
	_, nested := ctx.Value(cliStepContextKey{}).(*prettylog.Step)
	step := cliStep(ctx, title, prettylog.Actor(logging.Host), slog.String(logging.KeySpace, space))
	cmd.SetContext(lima.WithProgress(context.WithValue(ctx, cliStepContextKey{}, step), step.Detail()))
	defer cmd.SetContext(ctx)
	defer func() {
		cliEndStep(step, result)
		if nested {
			return
		}
		kind, message := prettylog.OutcomeOK, success
		if result != nil {
			kind, message = prettylog.OutcomeFailed, "Command failed"
			if cliUncertain(result) {
				kind, message = prettylog.OutcomeUnknown, "Command outcome is unknown"
			}
		}
		logging.Logger().Outcome(kind, message, prettylog.Actor(logging.Host), slog.String(logging.KeySpace, space), prettylog.Err(result))
		if result != nil && logging.Logger().Err() == nil {
			result = &cliRenderedError{result}
		}
	}()
	return run()
}

func cliRunStep(ctx context.Context, title string, run func(context.Context) error, attrs ...slog.Attr) error {
	step := cliStep(ctx, title, attrs...)
	ctx = lima.WithProgress(context.WithValue(ctx, cliStepContextKey{}, step), step.Detail())
	err := run(ctx)
	cliEndStep(step, err)
	return err
}

// cliFinishCommand runs only after the routed controller call returns its
// final response. Intermediate worker progress must not claim success here.
func cliFinishCommand(result error, success string) error {
	return cliFinishOutcome(result, success, logging.Controller)
}

func cliFinishOutcome(result error, success, actor string) error {
	var detached *controller.ClientDetached
	if errors.As(result, &detached) {
		return result
	} // separate attach-client status, never an uncertain mutation
	if result != nil && controller.OutcomeRendered(result) {
		return result
	}
	kind, message := prettylog.OutcomeOK, success
	if result != nil {
		kind, message = prettylog.OutcomeFailed, "Command failed"
		if cliUncertain(result) {
			kind, message = prettylog.OutcomeUnknown, "Command outcome is unknown"
		}
	}
	logging.Logger().Outcome(kind, message, prettylog.Actor(actor), prettylog.Err(result))
	if result != nil && logging.Logger().Err() == nil {
		return &cliRenderedError{result}
	}
	return result
}

func cliRoutedOutcome(cmd *cobra.Command, method string) bool {
	if method == "box.exec" || method == "box.attach" || method == "chat.stream" {
		return false
	}
	if flag := cmd.Flag("dry-run"); flag != nil && flag.Value.String() == "true" {
		return false
	}
	op, ok := controllerOperations[method]
	return ok && op.Mutates || method == "box.edit" || method == "account.edit"
}

// Host-only commands do not cross a receipt transport. Their final outcome
// belongs after RunE, including any deferred cleanup inside it.
func wrapHostLogCommand(cmd *cobra.Command, actor string) {
	run := cmd.RunE
	if run == nil {
		return
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) (result error) {
		if f := cmd.Flag("dry-run"); f == nil || f.Value.String() != "true" {
			defer func() { result = cliFinishOutcome(result, cmd.CommandPath()+" completed", actor) }()
		}
		return run(cmd, args)
	}
}

// suspendCLISteps gives an existing prompt/editor/attachment ownership of
// terminal output. It never adds an input reader or changes stream bytes.
func suspendCLISteps() func() {
	view := stepview.Current()
	view.Suspend()
	return view.Resume
}

func cliTerminalWriter(w io.Writer) io.Writer { return stepview.Current().Writer(w) }
