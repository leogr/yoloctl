package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// OlderProtocol is the protocol of every released controller before protocol
// 3 (v0.1.0 to v0.3.0). This build speaks it for one request only: the drain
// a host upgrade sends to such a running controller before it installs this
// build, so the old controller still finishes its operations first.
const OlderProtocol = 2

// RefusedAsOlder reports whether err is a released protocol-2 controller's
// answer to this build's hello: it exits with status 2 and this message before
// it reads any request, so nothing ran.
func RefusedAsOlder(err error) bool {
	var exit *ExitError
	return errors.As(err, &exit) && exit.Status == 2 &&
		exit.Message == fmt.Sprintf("controller protocol mismatch (client %d, controller %d)", ProtocolVersion, OlderProtocol)
}

// DrainOlder sends control.drain to a controller that RefusedAsOlder, in its
// protocol: hello carries the same bundle, space and generation at
// OlderProtocol, and the reply must be that protocol's hello with MatchHello's
// exact identity checks; the request carries only fields that protocol decodes
// (no Log). That controller
// answers with one exit frame once its drain has finished.
func DrainOlder(parent context.Context, conn io.ReadWriteCloser, hello Hello, id string, errw io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() { _ = conn.Close() }()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	hello.Protocol = OlderProtocol
	req := Request{ID: id, Method: "control.drain", Space: hello.Space, Dash: -1}
	if err := req.Validate(); err != nil {
		return err
	}
	wire := NewWire(conn, conn)
	if err := wire.Write(Frame{Type: "hello", Hello: &hello}); err != nil {
		return err
	}
	first, err := wire.Read()
	if err != nil {
		return fmt.Errorf("controller handshake: %w", err)
	}
	if first.Type == "exit" {
		return &ExitError{Status: first.Status, Message: first.Error}
	}
	if first.Type != "hello" || first.Hello == nil {
		return errors.New("invalid controller handshake")
	}
	remote := *first.Hello
	if remote.Protocol != OlderProtocol {
		return fmt.Errorf("controller protocol mismatch (client %d, controller %d)", OlderProtocol, remote.Protocol)
	}
	// Bundle, space and generation keep MatchHello's exact check.
	local := hello
	local.Protocol, remote.Protocol = ProtocolVersion, ProtocolVersion
	if err := MatchHello(local, remote); err != nil {
		return err
	}
	if err := wire.Write(Frame{Type: "request", Request: &req}); err != nil {
		return &OutcomeUnknown{ID: req.ID, Cause: err, Method: req.Method, Space: req.Space}
	}
	for {
		f, err := wire.Read()
		if err != nil {
			return &OutcomeUnknown{ID: req.ID, Cause: err, Method: req.Method, Space: req.Space}
		}
		switch f.Type {
		case "stderr":
			if errw != nil {
				if err := writeAll(errw, f.Data); err != nil {
					return err
				}
			}
		case "exit":
			if f.Status == 0 && f.Error == "" {
				return nil
			}
			return &ExitError{Status: f.Status, Message: f.Error}
		default:
			return &OutcomeUnknown{ID: req.ID, Cause: errors.New("unexpected controller response"), Method: req.Method, Space: req.Space}
		}
	}
}
