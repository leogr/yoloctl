// Package sshtransport runs bounded SSH transports without a host SSH program.
package sshtransport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// Run owns conn. The caller supplies the endpoint identity and authentication;
// this layer never looks at host SSH configuration, agents, or user identities.
func Run(ctx context.Context, conn net.Conn, config *ssh.ClientConfig, command string, pty bool, stdin *os.File, stdout, stderr io.Writer) error {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	client, err := handshake(ctx, conn, config)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return RunClient(ctx, client, command, pty, stdin, stdout, stderr)
}

// RunClient runs one session on an already authenticated, caller-owned
// connection. Cancellation closes that connection, including a blocked channel
// open or exec request; the caller may reuse it only after a successful run.
func RunClient(ctx context.Context, client *ssh.Client, command string, pty bool, stdin *os.File, stdout, stderr io.Writer) error {
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	s, err := client.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	s.Stdout, s.Stderr = stdout, stderr
	input, err := s.StdinPipe()
	if err != nil {
		return err
	}
	if pty && term.IsTerminal(int(stdin.Fd())) {
		name := os.Getenv("TERM")
		if name == "" {
			name = "xterm-256color"
		}
		if err = requestPTY(s, stdin, name); err != nil {
			return err
		}
		state, err := term.MakeRaw(int(stdin.Fd()))
		if err != nil {
			return err
		}
		defer func() { _ = term.Restore(int(stdin.Fd()), state) }()
		stopResize := resizeSession(s, stdin)
		defer stopResize()
	}
	if command == "" {
		err = s.Shell()
	} else {
		err = s.Start(command)
	}
	if err != nil {
		return err
	}
	// A session can finish before stdin (an interactive terminal) reaches EOF.
	// Polling a duplicated descriptor gives that copier its own cancellation.
	copyCtx, cancelInput := context.WithCancel(ctx)
	defer cancelInput()
	inputDone := make(chan struct{})
	go func() { defer close(inputDone); _ = copyInput(copyCtx, input, stdin); _ = input.Close() }()
	err = s.Wait()
	cancelInput()
	_ = s.Close()
	<-inputDone
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Connect owns conn and returns a client for a caller that runs several
// sessions over one authenticated connection. ctx and the same ten-second
// limit as Run bound only the handshake; closing the client closes conn.
func Connect(ctx context.Context, conn net.Conn, config *ssh.ClientConfig) (*ssh.Client, error) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	client, err := handshake(ctx, conn, config)
	if !stop() && err == nil {
		_ = client.Close()
		err = ctx.Err()
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return client, nil
}

func handshake(ctx context.Context, conn net.Conn, config *ssh.ClientConfig) (*ssh.Client, error) {
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, "yoloctl", config)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = cc.Close()
		return nil, err
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

// ExitStatus distinguishes a remote command failure from a transport failure.
func ExitStatus(err error) (int, bool) {
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return exit.ExitStatus(), true
	}
	return 0, false
}
