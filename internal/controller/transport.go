package controller

import (
	"io"
	"os/exec"
	"sync"
	"time"
)

// StartTransport uses a fixed trusted bridge command. Request data travels on
// stdin and is never interpolated into its arguments.
func StartTransport(cmd *exec.Cmd) (io.ReadWriteCloser, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return nil, err
	}
	return &commandTransport{in: in, out: out, cmd: cmd}, nil
}

type commandTransport struct {
	in   io.WriteCloser
	out  io.ReadCloser
	cmd  *exec.Cmd
	once sync.Once
}

func (c *commandTransport) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *commandTransport) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *commandTransport) Close() error {
	c.once.Do(func() {
		_ = c.in.Close()
		_ = c.out.Close()
		// EOF lets the SSH/Unix bridge finish normally after the final
		// response. Killing it immediately races its own successful exit
		// and can print teardown errors. Still bound a stuck bridge.
		done := make(chan struct{})
		go func() { _ = c.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = c.cmd.Process.Kill()
			<-done
		}
	})
	return nil
}
