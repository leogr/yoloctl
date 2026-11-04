package session

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/terminal"
)

// ExitError carries the session command's exit status delivered by the
// holder's EXIT frame. Attach returns it (wrapped) for any nonzero status
// so callers can propagate the session's code to the host shell. The
// session command is the box's agent — or the built-in login shell for a
// generic-shell box — so the message says "session", not "agent".
type ExitError struct {
	Status int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("session exited with status %d", e.Status)
}

// ExitCode returns the agent's exit status, letting the CLI's exit-code
// classifier propagate it to the host shell (kubectl-style).
func (e *ExitError) ExitCode() int { return e.Status }

// ErrSessionClosed reports that the holder connection ended without an
// EXIT frame: the holder (or the bridge to it) went away while the agent
// presumably kept running. Detach-like, but initiated by the far end.
var ErrSessionClosed = errors.New("session: connection to holder closed without an exit status")

// AttachIO is the local terminal side of an attach.
type AttachIO struct {
	Stdin  io.Reader
	Stdout io.Writer

	// Resize delivers text-grid geometry updates to forward to the
	// holder. May be nil. The caller should send the current size
	// immediately after Attach starts so the holder's PTY matches.
	Resize <-chan terminal.Geometry
	// OnEvent selects the managed snapshot view. Nil selects compatibility output.
	OnEvent func(ViewEvent) error
	Colors  <-chan terminal.Colors
	// Host enables live, explicit desktop requests and user-initiated uploads.
	// Nil preserves the ordinary terminal-only behavior.
	Host *HostClient
}

// Attach speaks the attach protocol over transport until the agent
// exits, the context is canceled, or the connection drops.
//
// Returns nil when the agent exited 0, *ExitError for a nonzero exit,
// ctx.Err() on cancellation (a detach: the agent keeps running), and
// ErrSessionClosed when the stream ended exitless. The transport is
// closed on return.
func Attach(ctx context.Context, transport io.ReadWriteCloser, aio AttachIO) error {
	return attachView(ctx, transport, aio)
}
