package processio

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

// Capture runs cmd, copying its stderr and, if combined, its stdout to output.
// Owning this pipe lets cancellation interrupt its read even after the command
// has exited, independently of exec.Cmd.WaitDelay's process termination grace.
// A positive drainLimit bounds the read after exit. With zero, ctx bounds it.
func Capture(ctx context.Context, cmd *exec.Cmd, output io.Writer, combined bool, drainLimit time.Duration) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	cmd.Stderr = writer
	if combined {
		cmd.Stdout = writer
	}
	err = cmd.Start()
	_ = writer.Close()
	if err != nil {
		return err
	}

	stop := context.AfterFunc(ctx, func() { _ = reader.Close() })
	defer stop()
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(output, reader)
		copied <- err
	}()
	waitErr := cmd.Wait()

	var expiry <-chan time.Time
	if drainLimit > 0 {
		timer := time.NewTimer(drainLimit)
		defer timer.Stop()
		expiry = timer.C
	}
	select {
	case readErr := <-copied:
		if cause := ctx.Err(); cause != nil {
			return errors.Join(waitErr, cause)
		}
		return errors.Join(waitErr, readErr)
	case <-expiry:
		_ = reader.Close()
		<-copied
		if cause := ctx.Err(); cause != nil {
			return errors.Join(waitErr, cause)
		}
		return errors.Join(waitErr, exec.ErrWaitDelay)
	}
}
