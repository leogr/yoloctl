//go:build linux

package vmguest

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"time"
)

var errShareMountDeadline = errors.New("host grant mount timed out")

// startShareTransport keeps Wait independent of the vsock pumps. The child
// has file descriptors, so Wait can reap it even while the host is silent.
func startShareTransport(cmd *exec.Cmd, conn net.Conn, drainTimeout time.Duration) (done <-chan error, abort func(), err error) {
	in, input, err := os.Pipe()
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	out, output, err := os.Pipe()
	if err != nil {
		_ = conn.Close()
		_ = in.Close()
		_ = input.Close()
		return nil, nil, err
	}
	closeTransport := func() {
		_ = conn.Close()
		_ = input.Close()
		_ = out.Close()
	}
	cmd.Stdin, cmd.Stdout = in, output
	err = startOwned(cmd)
	_ = in.Close()
	_ = output.Close()
	if err != nil {
		closeTransport()
		return nil, nil, err
	}
	received, sent := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(received)
		_, _ = io.Copy(input, conn)
		_ = input.Close()
	}()
	go func() {
		defer close(sent)
		_, _ = io.Copy(conn, out)
		_ = out.Close()
	}()
	result := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		ownedChildren.Delete(cmd.Process.Pid)
		_ = input.Close()
		if err := conn.SetReadDeadline(time.Now()); err != nil {
			_ = conn.Close()
		}
		// Give a reading host the final bytes even after the child exits.
		timer := time.NewTimer(drainTimeout)
		select {
		case <-sent:
		case <-timer.C:
		}
		timer.Stop()
		closeTransport()
		<-received
		<-sent
		result <- err
	}()
	return result, func() {
		_ = cmd.Process.Kill()
		closeTransport()
	}, nil
}

func awaitShare(dest string, mounted func() bool, done <-chan error, deadline <-chan time.Time, abort func(), onExit func(error)) error {
	recheck := time.NewTicker(time.Millisecond)
	defer recheck.Stop()
	for {
		if mounted() {
			go func() { onExit(<-done) }()
			return nil
		}
		select {
		case err := <-done:
			return fmt.Errorf("mount host grant: %w", err)
		case <-deadline:
			abort()
			return fmt.Errorf("%w: %s", errShareMountDeadline, dest)
		case <-recheck.C:
		}
	}
}
