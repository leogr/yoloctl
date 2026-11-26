package followview

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const vmReaderDrainFailSafe = 2 * time.Minute

// Own all three pipes. Giving exec a Reader or buffered Writer would give
// its Wait an internal copier that can remain blocked after the leader exits.
func runVMReaderCommand(ctx context.Context, cmd *exec.Cmd, input []byte, stdout, stderr io.Writer, drain time.Duration) error {
	var pipes [3]struct{ read, write *os.File }
	for i := range pipes {
		read, write, err := os.Pipe()
		if err != nil {
			return err
		}
		pipes[i].read, pipes[i].write = read, write
		defer func() { _ = read.Close() }()
		defer func() { _ = write.Close() }()
	}
	stdinPipe, stdoutPipe, stderrPipe := pipes[0], pipes[1], pipes[2]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinPipe.read, stdoutPipe.write, stderrPipe.write
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = stdinPipe.read.Close()
	_ = stdoutPipe.write.Close()
	_ = stderrPipe.write.Close()

	closePipes := func() {
		_ = stdinPipe.write.Close()
		_ = stdoutPipe.read.Close()
		_ = stderrPipe.read.Close()
	}
	stop := context.AfterFunc(ctx, closePipes)
	defer stop()

	var jobs sync.WaitGroup
	var copyErrors [3]error
	jobs.Go(func() {
		_, copyErrors[0] = io.Copy(stdinPipe.write, bytes.NewReader(input))
		_ = stdinPipe.write.Close()
	})
	jobs.Go(func() { _, copyErrors[1] = io.Copy(stdout, stdoutPipe.read) })
	jobs.Go(func() { _, copyErrors[2] = io.Copy(stderr, stderrPipe.read) })
	ioDone := make(chan struct{})
	go func() {
		jobs.Wait()
		close(ioDone)
	}()

	err := cmd.Wait()
	timer := time.NewTimer(drain)
	defer timer.Stop()
	select {
	case <-ioDone:
	case <-ctx.Done():
		closePipes()
		<-ioDone
	case <-timer.C:
		// A completing drain need not lose a complete response because
		// the deadline became ready in the same scheduler turn.
		select {
		case <-ioDone:
		default:
			closePipes()
			<-ioDone
			if err == nil {
				err = exec.ErrWaitDelay
			}
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		// As with exec's standard stdin copier, a child that closed its
		// input early can still have produced a valid complete reply.
		if errors.Is(copyErrors[0], syscall.EPIPE) {
			copyErrors[0] = nil
		}
		for _, copyErr := range copyErrors {
			if copyErr != nil {
				return copyErr
			}
		}
	}
	return err
}
