package controller

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

// logWorker is the test worker for the structured log pipe (TestMain).
func logWorker(work WorkerInput, finishExit func(int)) {
	file := OpenWorkerLog(work)
	if file == nil {
		finishExit(92)
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		finishExit(93) // a tool started by the worker would inherit the pipe
	}
	verbosity, _ := LogVerbosity(work.Request.Log)
	l := prettylog.New(file, prettylog.Options{Format: prettylog.FormatWire, Verbosity: verbosity})
	switch work.Request.Args[0] {
	case "records":
		step := l.StartStep("Building image test", prettylog.Actor("builder"))
		_, _ = io.WriteString(step.Detail(), "tool output\n")
		step.Done()
		l.Slog().Debug("debug detail")
		l.Outcome(prettylog.OutcomeFailed, "Box dev failed")
		finishExit(3)
	case "partial":
		l.Slog().Info("complete record")
		_, _ = file.Write([]byte{0, 0, 0, 9, '{'})
		finishExit(0)
	case "escaped":
		// A descendant outside the worker's process group keeps a duplicate
		// of the pipe open after the worker exits. Production workers claim
		// the payload/checkpoint pair close-on-exec; this fixture does too.
		unix.CloseOnExec(3)
		unix.CloseOnExec(4)
		child := exec.Command("sleep", "5")
		child.ExtraFiles = []*os.File{file}
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if child.Start() != nil {
			finishExit(94)
		}
		l.Slog().Info("before exit")
		finishExit(0)
	}
	finishExit(95)
}

type recordLog struct{ records []slog.Record }

func (r *recordLog) Handle(_ context.Context, rec slog.Record) error {
	r.records = append(r.records, rec)
	return nil
}
