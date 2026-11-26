// Package execpipe runs a command whose output pipes the caller owns, so the
// caller's context also ends the wait for output after the command exits.
//
// exec.Cmd copies a non-file writer through a pipe it owns and, once the
// process has exited, waits for that pipe's EOF or for WaitDelay, whatever the
// context does. A descendant that keeps the pipe open can therefore hold Wait
// past a cancelled context. Here each writer is fed from an os.Pipe that Run
// closes when ctx ends, and Wait returns as soon as the process exits.
package execpipe

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ErrOutputHeld reports that the command exited but a descendant kept one of
// its output pipes open past the drain bound. The output is incomplete.
var ErrOutputHeld = errors.New("command exited but its output is still held open")

// Run starts cmd with its stdout and stderr fed to the given writers (nil
// discards that stream; any output cmd already had is replaced), waits for
// the process to exit and for each stream to reach EOF, and returns the
// combined error.
//
// ctx bounds the whole run. drain, when positive, bounds only the wait for
// EOF after the process has exited: it starts at the exit, so a command may
// run as long as ctx allows. When ctx ends or the drain bound passes first,
// the read ends are closed at once and Run returns ctx's error or
// ErrOutputHeld: the output then is incomplete and must not be used. A writer
// error stops that stream and closes its read end, so the command sees a
// broken pipe, as with exec's own copying. cmd must be built with the same
// ctx (exec.CommandContext) so that the process is also stopped.
func Run(ctx context.Context, cmd *exec.Cmd, drain time.Duration, stdout, stderr io.Writer) error {
	cmd.Stdout, cmd.Stderr = nil, nil
	type stream struct {
		read  *os.File
		write *os.File
		to    io.Writer
	}
	var streams []*stream
	closeWrites := func() {
		for _, s := range streams {
			_ = s.write.Close()
		}
	}
	closeReads := func() {
		for _, s := range streams {
			_ = s.read.Close()
		}
	}
	for _, out := range []struct {
		to  io.Writer
		set func(*os.File)
	}{
		{stdout, func(f *os.File) { cmd.Stdout = f }},
		{stderr, func(f *os.File) { cmd.Stderr = f }},
	} {
		if out.to == nil {
			continue
		}
		r, w, err := os.Pipe()
		if err != nil {
			closeReads()
			closeWrites()
			return err
		}
		streams = append(streams, &stream{read: r, write: w, to: out.to})
		out.set(w)
	}
	if err := ctx.Err(); err != nil {
		closeReads()
		closeWrites()
		return err
	}
	if err := cmd.Start(); err != nil {
		closeReads()
		closeWrites()
		return err
	}
	// The child holds its own copies of the write ends.
	closeWrites()
	stop := context.AfterFunc(ctx, closeReads)
	defer stop()
	errs := make([]error, len(streams))
	var wg sync.WaitGroup
	for i, s := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = io.Copy(s.to, s.read)
			_ = s.read.Close()
		}()
	}
	waitErr := cmd.Wait()
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	held := false
	if drain > 0 {
		timer := time.NewTimer(drain)
		select {
		case <-drained:
		case <-timer.C:
			held = true
			closeReads()
			<-drained
		}
		timer.Stop()
	} else {
		<-drained
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, waitErr)
	}
	if held {
		return errors.Join(ErrOutputHeld, waitErr)
	}
	return errors.Join(append([]error{waitErr}, errs...)...)
}
