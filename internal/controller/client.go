package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
)

type ClientIO struct {
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	Resize  <-chan Size
	Signals <-chan int
	View    io.ReadWriteCloser
}

type ExitError struct {
	Status  int
	Message string
}

func (e *ExitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("command exited with status %d", e.Status)
}

func (e *ExitError) ExitCode() int { return e.Status }

// OutcomeUnknown means no final receipt was received. Callers may query this
// ID over a new connection, but must never automatically repeat the operation.
type OutcomeUnknown struct {
	ID    string
	Cause error
}

func (e *OutcomeUnknown) Error() string {
	return fmt.Sprintf("connection ended before the result of request %s; outcome may be uncertain: %v", e.ID, e.Cause)
}
func (e *OutcomeUnknown) Unwrap() error { return e.Cause }

// Call relays one command. The caller owns cancellation of a blocking local
// input reader (for example a terminal). Closing the transport cancels remote
// work unless the controller already owns it: once an attach's setup effects
// are authorized, setup finishes without this client, so a timeout here is no
// proof that a launch failed. A signal forwarded before an attach hands off
// returns ClientDetached. EOF on stdin does not detach an output stream.
func Call(parent context.Context, conn io.ReadWriteCloser, hello Hello, req Request, streams ClientIO) error {
	if err := req.Validate(); err != nil {
		_ = conn.Close()
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() { _ = conn.Close() }()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	wire := NewWire(conn, conn)
	if err := wire.Write(Frame{Type: "hello", Hello: &hello}); err != nil {
		return err
	}
	first, err := wire.Read()
	if err != nil {
		return fmt.Errorf("controller handshake: %w", err)
	}
	if first.Type == "exit" {
		return &ExitError{first.Status, first.Error}
	}
	if first.Type != "hello" || first.Hello == nil {
		return errors.New("invalid controller handshake")
	}
	if err := MatchHello(hello, *first.Hello); err != nil {
		return err
	}
	if err := wire.Write(Frame{Type: "request", Request: &req}); err != nil {
		return &OutcomeUnknown{req.ID, err}
	}
	credits := make(chan struct{}, 8)
	for range cap(credits) {
		credits <- struct{}{}
	}
	viewCredits := make(chan struct{}, 8)
	for range cap(viewCredits) {
		viewCredits <- struct{}{}
	}
	if req.View {
		if streams.View == nil {
			return errors.New("missing session view channel")
		}
		defer func() { _ = streams.View.Close() }()
		go func() {
			buf := make([]byte, 32768)
			for {
				n, err := streams.View.Read(buf)
				if n > 0 {
					select {
					case <-ctx.Done():
						return
					case <-viewCredits:
					}
					if err := wire.Write(Frame{Type: "view-input", Data: buf[:n]}); err != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}
	if methodHasInput(req.Method) {
		go func() {
			if streams.In != nil {
				buf := make([]byte, 32768)
				for {
					n, err := streams.In.Read(buf)
					if n > 0 {
						select {
						case <-ctx.Done():
							return
						case <-credits:
						}
						if err := wire.Write(Frame{Type: "stdin", Data: buf[:n]}); err != nil {
							return
						}
					}
					if err != nil {
						break
					}
					if ctx.Err() != nil {
						return
					}
				}
			}
			_ = wire.Write(Frame{Type: "eof"})
		}()
		go func() {
			sizes, signals := streams.Resize, streams.Signals
			for {
				select {
				case <-ctx.Done():
					return
				case size, ok := <-sizes:
					if !ok {
						sizes = nil
						continue
					}
					if err := wire.Write(Frame{Type: "resize", Cols: size.Cols, Rows: size.Rows}); err != nil {
						return
					}
				case sig, ok := <-signals:
					if !ok {
						signals = nil
						continue
					}
					if err := wire.Write(Frame{Type: "signal", Signal: sig}); err != nil {
						return
					}
				}
			}
		}()
	}
	for {
		f, err := wire.Read()
		if err != nil {
			return &OutcomeUnknown{req.ID, err}
		}
		switch f.Type {
		case "view-ready":
			select {
			case viewCredits <- struct{}{}:
			default:
				return errors.New("invalid session input flow control")
			}
		case "view":
			if !req.View || streams.View == nil {
				return errors.New("unexpected session view output")
			}
			if err := writeAll(streams.View, f.Data); err != nil {
				return err
			}
		case "input-ready":
			select {
			case credits <- struct{}{}:
			default:
				return &OutcomeUnknown{req.ID, errors.New("invalid input flow control")}
			}
		case "stdout":
			if streams.Out != nil {
				if err := writeAll(streams.Out, f.Data); err != nil {
					return err
				}
			}
		case "stderr":
			if streams.Err != nil {
				if err := writeAll(streams.Err, f.Data); err != nil {
					return err
				}
			}
		case "exit":
			if f.ImageRetryJob != "" {
				return &ImageInterrupted{JobID: f.ImageRetryJob, Retryable: true, Cause: errors.New(f.Error)}
			}
			if f.Status == 0 && f.Error == "" {
				return nil
			}
			return &ExitError{f.Status, f.Error}
		case "detached":
			return &ClientDetached{ID: req.ID, Signal: f.Signal, Owned: f.Owned}
		default:
			return &OutcomeUnknown{req.ID, errors.New("unexpected controller response")}
		}
	}
}

// These administration methods answer without starting a worker or reading
// stream frames. Sending even an EOF frame leaves unread Unix-socket data at
// close, causing resets in an otherwise successful bridge. control.complete
// does run a worker and keeps the ordinary streaming path.
func methodHasInput(method string) bool {
	switch method {
	case "control.ping", "control.drain", "control.resume", "control.receipts", "control.status", "control.acknowledge", "control.acknowledge-all", "control.native-trace":
		return false
	default:
		return true
	}
}
