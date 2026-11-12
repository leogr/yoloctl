package lima

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/rotatelog"
	"golang.org/x/sys/unix"
)

const diagnosticLimit = 64 << 10
const diagnosticErrorLimit = 2 << 10

// hostLogLimit bounds host.log; one previous generation, host.log.1, is kept,
// so the log stays near twice this size. A single record (at most 64 KiB of
// raw bytes, quoted) always fits.
const hostLogLimit = 1 << 20

// LogDiagnostics records private host diagnostics without writing a terminal.
// Callers log a fixed operation label, never command argv, environment or stdin.
// A returned error is about the log only: callers never turn it into the
// failure of an operation that succeeded.
func LogDiagnostics(operation string, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > diagnosticLimit {
		raw = raw[len(raw)-diagnosticLimit:]
	}
	path, err := layout.HostDiagnosticsLogPath()
	if err != nil {
		return err
	}
	root, err := layout.ConfigRoot()
	if err != nil {
		return err
	}
	dir, err := openLogsDirectory(root)
	if err != nil {
		return fmt.Errorf("open host diagnostics directory %q: %w", filepath.Dir(path), err)
	}
	defer func() { _ = unix.Close(dir) }()
	// Quote diagnostic bytes: explicitly reading the log cannot replay controls.
	line := time.Now().UTC().Format(time.RFC3339Nano) + " " + strconv.Quote(operation) + " " + strconv.QuoteToASCII(string(raw)) + "\n"
	return appendHostLog(dir, filepath.Base(path), path, line)
}

const logsDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// openLogsDirectory creates or opens <root>/logs without following links and
// requires it to be owned by this user with mode 0700. Several yoloctl
// processes may create it at once, and a new directory first takes its
// parent's group on macOS (BSD semantics), until its creator changes it. With
// no group permission bits the group grants nothing, so it is not checked;
// the creator still sets the user's group.
func openLogsDirectory(root string) (int, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return -1, err
	}
	parent, err := unix.Open(root, logsDirectoryFlags, 0)
	if err != nil {
		return -1, err
	}
	defer func() { _ = unix.Close(parent) }()
	created := false
	if err := unix.Mkdirat(parent, "logs", 0o700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return -1, err
	}
	fd, err := unix.Openat(parent, "logs", logsDirectoryFlags, 0)
	if err != nil {
		return -1, err
	}
	if created {
		err = errors.Join(unix.Fchown(fd, os.Getuid(), os.Getgid()), unix.Fsync(fd), unix.Fsync(parent))
	}
	var st unix.Stat_t
	if err == nil {
		err = unix.Fstat(fd, &st)
	}
	if err == nil && (st.Uid != uint32(os.Getuid()) || st.Mode&0o777 != 0o700) {
		err = errors.New("logs directory must be owned by this user with mode 0700")
	}
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// appendHostLog appends one record under an exclusive lock on the current
// file. Several yoloctl processes may log at once: each rechecks, while
// holding the lock, that its descriptor is still the file at the name, so a
// concurrent rotation is never repeated and no record goes to a rotated file.
func appendHostLog(dir int, name, path, line string) error {
	return hostLogAppender().Append(dir, name, path, []byte(line))
}

const hostLogAttempts = rotatelog.Attempts

// openHostLog opens or creates host.log in the logs directory; tests replace it.
var openHostLog = func(dir int, name string) (int, error) {
	return unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
}

func hostLogAppender() rotatelog.Appender {
	return rotatelog.Appender{Limit: hostLogLimit, Label: "host diagnostics log", Sync: true, Open: openHostLog}
}

// recordDiagnostics logs a capture and decides what the caller returns: a
// failure to log never turns a successful operation into an error; when the
// operation already failed, the log problem is reported beside its error.
func recordDiagnostics(operation string, raw []byte, err error) error {
	logErr := LogDiagnostics(operation, raw)
	if err == nil || logErr == nil {
		return err
	}
	return errors.Join(err, fmt.Errorf("recording diagnostics also failed: %w", logErr))
}

// RecordDiagnostics is recordDiagnostics for callers outside this package.
func RecordDiagnostics(operation string, raw []byte, err error) error {
	return recordDiagnostics(operation, raw, err)
}

type diagnosticTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *diagnosticTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	if len(p) >= diagnosticLimit {
		t.buf = append(t.buf[:0], p[len(p)-diagnosticLimit:]...)
	} else {
		if drop := len(t.buf) + len(p) - diagnosticLimit; drop > 0 {
			t.buf = t.buf[:copy(t.buf, t.buf[drop:])]
		}
		t.buf = append(t.buf, p...)
	}
	return n, nil
}

func (t *diagnosticTail) bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}

type progressContextKey struct{}

type progressWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Presentation failures must not stop draining the launcher pipe.
	_, _ = w.writer.Write(p)
	return len(p), nil
}

// WithProgress routes lifecycle launcher diagnostics to the current operation.
// Explicit command stdout consumers remain authoritative.
func WithProgress(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, progressContextKey{}, w)
}

func progressFrom(ctx context.Context) io.Writer {
	w, _ := ctx.Value(progressContextKey{}).(io.Writer)
	return w
}

// runWithDiagnostics preserves explicit stdout consumers such as JSON readers.
func runWithDiagnostics(cmd *exec.Cmd) error { return runWithProgress(cmd, nil) }

// Owned OS pipes avoid os/exec copier waits when daemon descendants retain FDs.
func runWithProgress(cmd *exec.Cmd, progress io.Writer) error {
	if progress != nil {
		progress = &progressWriter{writer: progress}
	}
	var tail, stdoutTail diagnosticTail
	type capture struct {
		reader, writer *os.File
		done           chan struct{}
	}
	var captures []capture
	open := func(dst io.Writer) (*os.File, error) {
		pr, pw, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		done := make(chan struct{})
		captures = append(captures, capture{pr, pw, done})
		go func() { _, _ = io.Copy(dst, pr); close(done) }()
		return pw, nil
	}
	var stderr io.Writer = &tail
	if progress != nil {
		stderr = io.MultiWriter(&tail, progress)
	}
	pw, err := open(stderr)
	if err != nil {
		return fmt.Errorf("capture limactl stderr: %w", err)
	}
	cmd.Stderr = pw
	if progress != nil && cmd.Stdout == os.Stdout {
		out, openErr := open(io.MultiWriter(&stdoutTail, progress))
		if openErr != nil {
			for _, c := range captures {
				_ = c.writer.Close()
				_ = c.reader.Close()
				<-c.done
			}
			return fmt.Errorf("capture limactl stdout: %w", openErr)
		}
		cmd.Stdout = out
	}
	err = cmd.Run()
	for _, c := range captures {
		_ = c.writer.Close()
	}
	deadline := time.NewTimer(stderrDrainGrace)
	defer deadline.Stop()
	drained := true
	for _, c := range captures {
		if drained {
			select {
			case <-c.done:
			case <-deadline.C:
				drained = false
			}
		}
	}
	for _, c := range captures {
		_ = c.reader.Close()
		<-c.done
	}

	raw := tail.bytes()
	operation := "limactl"
	if len(cmd.Args) > 1 {
		operation += " " + cmd.Args[1]
	}
	if output := stdoutTail.bytes(); len(output) != 0 {
		err = recordDiagnostics(operation+" stdout", output, err)
		raw = append(output, raw...)
	}
	err = recordDiagnostics(operation, tail.bytes(), err)
	if err == nil {
		return nil
	}
	if len(raw) > diagnosticErrorLimit {
		raw = raw[len(raw)-diagnosticErrorLimit:]
	}
	detail := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(raw), ""))), " ")
	if detail == "" {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w: %s", operation, err, detail)
}
