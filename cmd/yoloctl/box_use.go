package main

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/boxuse"
	"github.com/leogr/yoloctl/internal/config"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/muesli/cancelreader"
	"github.com/spf13/cobra"
)

// closingView initiates graceful worker detach before closing the frontend end.
// The controller keeps draining final view bytes to obtain its operation receipt.
type closingView struct {
	io.ReadWriteCloser
	once      sync.Once
	detaching *atomic.Bool
	signals   chan<- int
}

func (v *closingView) Close() error {
	v.once.Do(func() {
		v.detaching.Store(true)
		select {
		case v.signals <- int(syscall.SIGTERM):
		default:
		}
	})
	return v.ReadWriteCloser.Close()
}

type drainingView struct {
	io.ReadWriteCloser
	detaching *atomic.Bool
}

func (v *drainingView) Write(p []byte) (int, error) {
	if v.detaching.Load() {
		return len(p), nil
	}
	n, err := v.ReadWriteCloser.Write(p)
	if err != nil && v.detaching.Load() {
		return len(p), nil
	}
	return n, err
}

// boxUseInput keeps pipe and terminal reads cancellable. Regular files,
// including Bash's larger heredocs, and the null device cannot join epoll; they
// reach EOF and are read directly within each caller's limits. The caller
// keeps the descriptor.
func boxUseInput(file *os.File) (cancelreader.CancelReader, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().IsRegular() || isNullDevice(info) {
		return regularInput{file}, nil
	}
	return cancelreader.NewReader(file)
}

// isNullDevice identifies the opened descriptor itself, not its name: the null
// device opened through any path qualifies; other character devices do not.
func isNullDevice(info os.FileInfo) bool {
	if info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err == nil && null.Mode()&os.ModeCharDevice != 0 && os.SameFile(info, null)
}

// regularInput cannot interrupt a read in progress; callers still check their
// contexts. Closing it leaves the caller's file open.
type regularInput struct{ io.Reader }

func (regularInput) Cancel() bool { return false }
func (regularInput) Close() error { return nil }

func callControllerBoxUse(cmd *cobra.Command, target controllerTarget, req controller.Request) error {
	if req.Method != "box.attach" {
		return asUserError(errors.New("--box-use requires box attach"))
	}
	if !target.Operator {
		prefs, err := config.LoadTUI()
		if err != nil {
			return err
		}
		req.Session = &controller.SessionPreferences{RingBytes: prefs.Session.RingBufferBytes, TitleTemplate: prefs.GetBoxTitleTemplate(), AgentIcons: prefs.AgentIconGlyphs(), Terminal: termio.CurrentTerminalIdentity()}
	}
	local, remote, err := termio.ViewPair()
	if err != nil {
		return err
	}
	defer func() { _ = local.Close(); _ = remote.Close() }()
	input, send := io.Pipe()
	defer func() { _ = input.Close(); _ = send.Close() }()
	signals := make(chan int, 1)
	var detaching atomic.Bool
	view := &closingView{ReadWriteCloser: local, detaching: &detaching, signals: signals}
	// Finish the receipt even when the caller cancels its frontend. Cleanup is
	// bounded below; failure remains explicit and is never replayed.
	ctx, cancel := context.WithCancel(context.WithoutCancel(cmd.Context()))
	defer cancel()
	frontendInput := cmd.InOrStdin()
	if f, ok := frontendInput.(*os.File); ok {
		reader, err := boxUseInput(f)
		if err != nil {
			cancel()
			return err
		}
		defer func() { reader.Cancel(); _ = reader.Close() }()
		frontendInput = reader
	}
	done := make(chan error, 1)
	req.View, req.Terminal, req.Term = true, false, "xterm-256color"
	go func() {
		err := target.Call(ctx, req, controller.ClientIO{In: input, Out: cmd.ErrOrStderr(), Err: cmd.ErrOrStderr(),
			View: &drainingView{ReadWriteCloser: remote, detaching: &detaching}, Signals: signals})
		_ = input.Close()
		_ = remote.Close()
		done <- err
	}()
	// Unblock an input write under PTY/controller backpressure on cancellation.
	stop := context.AfterFunc(cmd.Context(), func() { _ = send.Close(); _ = view.Close() })
	defer stop()
	frontErr := boxuse.Run(cmd.Context(), view, frontendInput, cmd.OutOrStdout(), send)
	_ = view.Close()
	_ = send.Close()
	var remoteErr error
	select {
	case remoteErr = <-done:
	case <-time.After(5 * time.Second):
		cancel()
		select {
		case remoteErr = <-done:
		case <-time.After(time.Second):
			return &controller.OutcomeUnknown{ID: req.ID, Cause: errors.New("timed out draining box-use operation receipt")}
		}
	}
	var unknown *controller.OutcomeUnknown
	if errors.As(remoteErr, &unknown) {
		return remoteErr
	}
	var detached *controller.ClientDetached
	if errors.As(remoteErr, &detached) {
		return reportClientDetached(cmd, req, remoteErr)
	}
	if errors.Is(frontErr, context.Canceled) {
		return nil
	}
	if frontErr != nil {
		var remote *controller.ExitError
		if errors.As(remoteErr, &remote) && remote.Message != "" {
			return remoteErr
		}
		return frontErr
	}
	// A frontend-requested detach can race the worker's view EOF. Its final
	// known status is consumed above; it does not mean the agent exited.
	return nil
}
