package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui"
	"github.com/muesli/cancelreader"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"
)

type controllerTarget struct {
	Space    string
	DiskID   string
	Operator bool
	Call     func(context.Context, controller.Request, controller.ClientIO) error
}

// Keep frontend mode detection replaceable in tests: an operator-enabled
// development box has real endpoint metadata outside the fixture's XDG roots.
// Server authorization remains independent of this routing metadata.
var loadOperatorClient = controller.LoadOperatorClient

func selectControllerTarget(ctx context.Context, cmd *cobra.Command) (controllerTarget, error) {
	var target controllerTarget
	operator, err := loadOperatorClient()
	if err != nil {
		return target, err
	}
	var space string
	if operator != nil {
		space = operator.Space
		if f := cmd.Flag("space"); f != nil && f.Changed {
			space = f.Value.String()
		}
		if space != operator.Space {
			return target, asUserError(errors.New("box operator is limited to its current space"))
		}
		identity, err := controller.ExecutableIdentity()
		if err != nil {
			return target, err
		}
		target = controllerTarget{Space: space, Operator: true, Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
			conn, err := (&net.Dialer{}).DialContext(ctx, "unix", controller.OperatorSocket)
			if err != nil {
				return fmt.Errorf("box operator endpoint unavailable: %w", err)
			}
			return controller.Call(ctx, conn, controller.Hello{Protocol: controller.ProtocolVersion, Bundle: identity, Space: space, Generation: operator.Generation}, req, streams)
		}}
		return target, nil
	}
	space, err = selectedSpace(cmd)
	if err != nil {
		return target, err
	}
	if commandNeedsVMCompatibility(cmd) {
		if err := ensureCommandVMCompatibility(cmd, space); err != nil {
			return target, err
		}
	}
	boot, err := checkControllerSpace(ctx, space)
	if err != nil {
		return target, err
	}
	return controllerTarget{Space: space, DiskID: boot.DiskID, Call: func(ctx context.Context, req controller.Request, streams controller.ClientIO) error {
		return callHostController(ctx, boot, req, streams)
	}}, nil
}

func commandMethod(cmd *cobra.Command) string {
	path := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	switch path {
	case "attach":
		return "box.attach"
	}
	return strings.ReplaceAll(path, " ", ".")
}

// routeControllerCommands replaces execution only; the same Cobra command
// definitions provide flags, help, aliases, argument validation and completion.
func routeControllerCommands(root *cobra.Command) {
	for _, cmd := range root.Commands() {
		routeControllerCommands(cmd)
	}
	method := commandMethod(root)
	_, ordinary := controllerOperations[method]
	if root.RunE == nil || (!ordinary && method != "box.edit" && method != "account.edit") || strings.HasPrefix(method, "space.") {
		return
	}
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if f := cmd.Flag("all-spaces"); f != nil && f.Value.String() == "true" {
			return controllerAllSpaces(cmd, method)
		}
		target, err := selectControllerTarget(cmd.Context(), cmd)
		if err != nil {
			return err
		}
		if target.Operator && !strings.HasPrefix(method, "box.") {
			return asUserError(errors.New("this operator endpoint grants only box commands"))
		}
		if method == "box.edit" || method == "account.edit" {
			return editThroughController(cmd, target, method, args)
		}
		req := controller.Request{ID: rand.Text(), Method: method, Space: target.Space, Args: append([]string(nil), args...), Dash: cmd.ArgsLenAtDash(), Flags: map[string][]string{}}
		req.Cwd, err = os.Getwd()
		if err != nil {
			return err
		}
		cmd.Flags().Visit(func(flag *pflag.Flag) {
			if slice, ok := flag.Value.(pflag.SliceValue); ok {
				req.Flags[flag.Name] = append([]string{}, slice.GetSlice()...)
			} else {
				req.Flags[flag.Name] = []string{flag.Value.String()}
			}
		})
		if !target.Operator {
			if err := expandControllerPaths(&req, target.DiskID); err != nil {
				return err
			}
		}
		if method == "account.auth.import" {
			file, _ := cmd.Flags().GetString("file")
			doc, err := prepareAuthImport(args, file, target.Space)
			if err != nil {
				return err
			}
			req.Payload, err = json.Marshal(doc)
			if err != nil {
				return err
			}
			delete(req.Flags, "file")
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "importing %s\n", doc.Source)
		}
		if method == "account.secret.set" {
			if err := prepareControllerSecret(cmd, &req); err != nil {
				return err
			}
		}
		if method == "box.copy" {
			from, _ := cmd.Flags().GetString("from-space")
			if from != "" && from != target.Space {
				if target.Operator {
					return asUserError(errors.New("box operators cannot copy across spaces"))
				}
				source, err := checkControllerSpace(cmd.Context(), from)
				if err != nil {
					return err
				}
				var raw bytes.Buffer
				get := controller.Request{ID: rand.Text(), Method: "box.copy-source", Space: from, Args: args[:1], Dash: -1}
				if err := callHostController(cmd.Context(), source, get, controller.ClientIO{Out: &raw, Err: cmd.ErrOrStderr()}); err != nil {
					return err
				}
				// Source is YAML; keep it as JSON-encoded bytes on the wire.
				req.Payload, err = json.Marshal(raw.Bytes())
				if err != nil {
					return err
				}
			}
		}
		return callControllerTerminal(cmd, target, req)
	}
}

// Environment and stdin belong to the caller. Transport secrets in the private
// request payload, never in remote argv or the controller's root environment.
func prepareControllerSecret(cmd *cobra.Command, req *controller.Request) error {
	if len(req.Args) != 2 {
		return errors.New("account and secret key are required")
	}
	fromEnv, _ := cmd.Flags().GetString("from-env")
	value, err := readCredentialValue(cmd, fromEnv, req.Args[1])
	if err != nil {
		return asUserError(err)
	}
	if len(value) > controller.MaxDocument {
		return asUserError(errors.New("secret exceeds size limit"))
	}
	req.Payload, err = json.Marshal(controllerSecret{Value: value})
	delete(req.Flags, "from-env")
	return err
}

func expandControllerPaths(req *controller.Request, diskID string) error {
	identity := &spaceconfig.Config{AccountDiskID: diskID}
	canonicalize := func(raw string) (string, error) {
		path, _, err := canonicalizeMountArg(raw)
		if err != nil {
			return "", err
		}
		// Match space-grant validation in the host namespace. The Linux worker
		// cannot resolve host aliases such as macOS's /var -> /private/var.
		// Worker-side protected-path and live-grant checks still apply.
		if err := rejectSpaceHostPath(path, identity); err != nil {
			return "", err
		}
		return path, nil
	}
	for i, raw := range req.Flags["workdir"] {
		if raw == "" {
			continue
		}
		path, err := canonicalize(raw)
		if err != nil {
			return err
		}
		req.Flags["workdir"][i] = path
	}
	for i, raw := range req.Flags["mount"] {
		if raw == "" {
			continue
		}
		source, rest, ok := strings.Cut(raw, ":")
		if !ok {
			return asUserError(errors.New("mount requires source:target[:ro]"))
		}
		path, err := canonicalize(source)
		if err != nil {
			return err
		}
		req.Flags["mount"][i] = path + ":" + rest
	}
	return nil
}

func editThroughController(cmd *cobra.Command, target controllerTarget, method string, args []string) error {
	var raw bytes.Buffer
	req := controller.Request{ID: rand.Text(), Method: method + ".get", Space: target.Space, Args: args, Dash: -1}
	if err := target.Call(cmd.Context(), req, controller.ClientIO{Out: &raw, Err: cmd.ErrOrStderr()}); err != nil {
		return err
	}
	var doc editDocument
	if err := decodeBoundedJSON(&raw, &doc, 2*controller.MaxDocument); err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "yoloctl-edit-*.yaml")
	if err != nil {
		return err
	}
	path := tmp.Name()
	retain := false
	defer func() {
		if !retain {
			_ = os.Remove(path)
		}
	}()
	if _, err := tmp.Write(doc.Data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := runEditor(cmd.Context(), path); err != nil {
		return err
	}
	edited, err := safepath.ReadFile(filepath.Dir(path), filepath.Base(path), controller.MaxDocument)
	if err != nil {
		return err
	}
	if bytes.Equal(edited, doc.Data) {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no changes")
		return nil
	}
	doc.Data = edited
	payload, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	req.ID, req.Method, req.Payload = rand.Text(), method+".apply", payload
	if err := target.Call(cmd.Context(), req, controller.ClientIO{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}); err != nil {
		retain = true
		return fmt.Errorf("edits kept at %s: %w", path, err)
	}
	return nil
}

var runAttachedView = tui.RunAttached

func managedBoxName(req controller.Request) string {
	switch req.Method {
	case "box.copy":
		if len(req.Args) > 1 {
			return req.Args[1]
		}
	default:
		if len(req.Args) > 0 {
			return req.Args[0]
		}
	}
	return "BOX"
}

func managedDetachHint(req controller.Request) string {
	return fmt.Sprintf("detached — the session keeps running; reattach: yoloctl box attach %s --space %s", managedBoxName(req), req.Space)
}

// reportClientDetached explains an attach client that a signal ended before
// its setup handed off. It is never an uncertain outcome: nothing fences and
// nothing is retried. The error keeps its distinct exit status.
func reportClientDetached(cmd *cobra.Command, req controller.Request, err error) error {
	var detached *controller.ClientDetached
	if !errors.As(err, &detached) {
		return err
	}
	cmd.SilenceErrors = true
	name := managedBoxName(req)
	if detached.Owned {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%v; nothing is retried\ncheck: yoloctl box session %s --space %s\nreattach: yoloctl box attach %s --space %s\n",
			err, name, req.Space, name, req.Space)
	} else {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%v; nothing is retried\n", err)
	}
	return err
}

func callControllerTerminal(cmd *cobra.Command, target controllerTarget, req controller.Request) error {
	passthrough, _ := cmd.Flags().GetBool("passthrough")
	boxUse, _ := cmd.Flags().GetBool("box-use")
	if boxUse {
		if passthrough {
			return asUserError(errors.New("--box-use and --passthrough are mutually exclusive"))
		}
		return callControllerBoxUse(cmd, target, req)
	}
	noAttach, _ := cmd.Flags().GetBool("no-attach")
	manifestOnly, _ := cmd.Flags().GetBool("manifest-only")
	if controller.ManagedSessionMethod(req.Method) && !passthrough && !noAttach && !manifestOnly && os.Getenv("YOLOCTL_TUI") != "1" && os.Getenv(termio.ViewFDEnv) == "" && stdinIsTerminal(cmd.InOrStdin()) && term.IsTerminal(int(os.Stdout.Fd())) {
		req.Flags = maps.Clone(req.Flags)
		if req.Flags == nil {
			req.Flags = map[string][]string{}
		}
		req.Flags["space"] = []string{req.Space}
		args, err := controllerCommandArgs(newRootCmd(), req)
		if err != nil {
			return err
		}
		detached, err := runAttachedView(cmd.Context(), args)
		var status interface{ ExitCode() int }
		if errors.As(err, &status) && status.ExitCode() >= 0 {
			err = &controller.ExitError{Status: status.ExitCode(), Message: err.Error()}
		}
		if detached {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), managedDetachHint(req))
		}
		return err
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	if op, ok := controllerOperations[req.Method]; ok && !op.Mutates && req.Method != "box.logs" {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, 30*time.Second)
		defer stop()
	}
	if !target.Operator {
		prefs, err := config.LoadTUI()
		if err != nil {
			return err
		}
		req.Session = &controller.SessionPreferences{RingBytes: prefs.Session.RingBufferBytes, TitleTemplate: prefs.GetBoxTitleTemplate(), AgentIcons: prefs.AgentIconGlyphs(), Terminal: termio.CurrentTerminalIdentity()}
	}
	view, err := termio.InheritedView()
	if err != nil {
		return err
	}
	if view != nil {
		defer func() { _ = view.Close() }()
	}
	req.Term = os.Getenv("TERM")
	streams := controller.ClientIO{In: cmd.InOrStdin(), Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
	if view != nil && !passthrough && controller.ManagedSessionMethod(req.Method) {
		req.View = true
		streams.View = view
	}
	signals := make(chan int, 4)
	streams.Signals = signals
	stopSignals := forwardCommandSignals(ctx, signals)
	defer stopSignals()
	if req.Method == "account.secret.set" {
		streams.In = nil // already captured locally; no terminal or second read
	}
	if input, ok := streams.In.(*os.File); ok && term.IsTerminal(int(input.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		req.Terminal = true
		cols, rows, err := term.GetSize(int(input.Fd()))
		if err != nil {
			return err
		}
		req.Cols, req.Rows = uint16(cols), uint16(rows)
		old, err := term.MakeRaw(int(input.Fd()))
		if err != nil {
			return err
		}
		defer func() { _ = term.Restore(int(input.Fd()), old) }()
		// MakeRaw disables the terminal's LF -> CRLF output processing too.
		// Preparation/CLI diagnostics are plain text; session stdout and the
		// private view already carry their own terminal bytes and must bypass
		// this adapter. Redirected stderr must retain its original bytes.
		if writerIsTerminal(streams.Err) {
			streams.Err = &rawTerminalStderr{out: streams.Err}
		}
		reader, err := cancelreader.NewReader(input)
		if err != nil {
			return err
		}
		defer func() { reader.Cancel(); _ = reader.Close() }()
		streams.In = reader
		sizes := make(chan controller.Size, 1)
		streams.Resize = sizes
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-winch:
					cols, rows, err := term.GetSize(int(input.Fd()))
					if err != nil {
						continue
					}
					select {
					case <-sizes:
					default:
					}
					select {
					case sizes <- controller.Size{Cols: uint16(cols), Rows: uint16(rows)}:
					default:
					}
				}
			}
		}()
	}
	if !target.Operator && os.Getenv("YOLOCTL_TUI") != "1" && term.IsTerminal(int(os.Stdout.Fd())) {
		prefs, err := config.LoadTUI()
		if err != nil {
			return err
		}
		filter := termio.NewOSC52Filter(streams.Out, func(event termio.OSC52) {
			sequence, notice := termio.EvaluateOSC52(event, prefs.ClipboardEnabled())
			if sequence != "" {
				_, _ = io.WriteString(cmd.OutOrStdout(), sequence)
			}
			if notice != "" {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), notice)
			}
		})
		defer func() { _ = filter.Flush() }()
		streams.Out = filter
	}
	err = callWithImageRetry(ctx, cmd, req, streams, target.Call)
	var remote *controller.ExitError
	if errors.As(err, &remote) && remote.Message == "" {
		// The shared remote CLI has already printed its error. Preserve the
		// status without adding a second generic "command exited" message.
		cmd.SilenceErrors = true
	}
	return err
}

// Only typed controller evidence that both job and request lacked permission
// enables a human retry. Every yes sends a new request through normal routing.
func callWithImageRetry(ctx context.Context, cmd *cobra.Command, req controller.Request, streams controller.ClientIO, call func(context.Context, controller.Request, controller.ClientIO) error) error {
	for {
		err := reportClientDetached(cmd, req, call(ctx, req, streams))
		var interrupted *controller.ImageInterrupted
		if !errors.As(err, &interrupted) || !interrupted.Retryable || ctx.Err() != nil {
			return err
		}
		if !stdinIsTerminal(cmd.InOrStdin()) || !writerIsTerminal(cmd.OutOrStdout()) {
			return err
		}
		again, promptErr := promptYesNo("retry? [y/N]: ", cmd.InOrStdin(), cmd.ErrOrStderr())
		if promptErr != nil {
			return promptErr
		}
		if !again {
			return err
		}
		req.ID = rand.Text()
	}
}
func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}
