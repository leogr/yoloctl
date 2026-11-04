package main

import (
	"errors"
	"os/exec"
	"syscall"
)

// userError marks an error as caused by user input (bad flag, invalid
// name, missing required arg, "no such box", etc.). The CLI maps it
// to exit code 1 in main; everything else falls through to 2 (runtime).
//
// Wrapping is transparent — Error() and Unwrap() pass through to the
// underlying error so the user-facing message is unchanged. The marker
// is detected via IsUserError, which uses errors.As to walk the chain.
type userError struct{ err error }

func (e userError) Error() string { return e.err.Error() }
func (e userError) Unwrap() error { return e.err }

// asUserError wraps err as a user error. Returns nil if err is nil so
// callers can write `return asUserError(maybeNil())` ergonomically.
func asUserError(err error) error {
	if err == nil {
		return nil
	}
	return userError{err: err}
}

// IsUserError reports whether err was produced by user input. Used by
// main.go to choose an exit code.
func IsUserError(err error) bool {
	var target userError
	return errors.As(err, &target)
}

// passthroughExitError marks a user-facing child process whose exit
// status is yoloctl's result, not an internal runtime failure. Use this
// only at command boundaries with exec-like contracts (`space ssh`,
// `agentbox exec`), never for implementation helpers such as limactl
// start, nerdctl, npm, tar, etc.
type passthroughExitError struct {
	err  error
	code int
}

func (e passthroughExitError) Error() string            { return e.err.Error() }
func (e passthroughExitError) Unwrap() error            { return e.err }
func (e passthroughExitError) passthroughExitCode() int { return e.code }

// asPassthroughExitError wraps err when it carries a concrete subprocess
// exit status. Start/lookup failures are returned unchanged so they stay
// runtime errors.
func asPassthroughExitError(err error) error {
	if err == nil {
		return nil
	}
	code, ok := subprocessExitCode(err)
	if !ok {
		return err
	}
	return passthroughExitError{err: err, code: code}
}

func subprocessExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil {
		return 0, false
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), true
	}
	code := exitErr.ExitCode()
	if code < 0 {
		return 0, false
	}
	return code, true
}
