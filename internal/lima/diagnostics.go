package lima

import (
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
	"github.com/leogr/yoloctl/internal/safepath"
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
	if err := safepath.PrivateDirectory(root, "logs", os.Getuid(), os.Getgid()); err != nil {
		return fmt.Errorf("open host diagnostics directory %q: %w", filepath.Dir(path), err)
	}
	dir, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dir) }()
	// Quote diagnostic bytes: explicitly reading the log cannot replay controls.
	line := time.Now().UTC().Format(time.RFC3339Nano) + " " + strconv.Quote(operation) + " " + strconv.QuoteToASCII(string(raw)) + "\n"
	return appendHostLog(dir, filepath.Base(path), path, line)
}

// appendHostLog appends one record under an exclusive lock on the current
// file. Several yoloctl processes may log at once: each rechecks, while
// holding the lock, that its descriptor is still the file at the name, so a
// concurrent rotation is never repeated and no record goes to a rotated file.
func appendHostLog(dir int, name, path, line string) error {
	var openErr error
	for range hostLogAttempts {
		fd, err := openHostLog(dir, name)
		if errors.Is(err, unix.ENOENT) {
			// macOS can report ENOENT for O_CREAT while another writer
			// renames the name away; the next attempt opens the new file.
			openErr = err
			continue
		}
		if err != nil {
			return fmt.Errorf("open host diagnostics log %q: %w", path, err)
		}
		f := os.NewFile(uintptr(fd), path)
		retry, err := appendLocked(f, fd, dir, name, path, line)
		_ = f.Close()
		if err != nil || !retry {
			return err // includes a failed sync after rotation
		}
	}
	if openErr != nil {
		return fmt.Errorf("open host diagnostics log %q: %w", path, openErr)
	}
	return fmt.Errorf("host diagnostics log %q kept changing while appending", path)
}

// hostLogAttempts bounds the opens of one append across concurrent
// rotations and spurious ENOENT from O_CREAT racing a rename.
const hostLogAttempts = 4

// openHostLog opens or creates host.log in the logs directory; tests replace it.
var openHostLog = func(dir int, name string) (int, error) {
	return unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
}

func appendLocked(f *os.File, fd, dir int, name, path, line string) (bool, error) {
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return false, err
	}
	var st, named unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0777 != 0600 || st.Nlink != 1 {
		return false, fmt.Errorf("host diagnostics log %q must be an owned private regular file", path)
	}
	if err := unix.Fstatat(dir, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named.Dev != st.Dev || named.Ino != st.Ino {
		return true, nil // rotated or replaced meanwhile: reopen the current file
	}
	if st.Size > 0 && st.Size+int64(len(line)) > hostLogLimit {
		// Keep one previous generation; renaming never follows a planted name.
		if err := unix.Renameat(dir, name, dir, name+".1"); err != nil {
			return false, fmt.Errorf("rotate host diagnostics log %q: %w", path, err)
		}
		return true, unix.Fsync(dir)
	}
	if _, err := io.WriteString(f, line); err != nil {
		return false, err
	}
	return false, errors.Join(f.Sync(), unix.Fsync(dir))
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

// runWithDiagnostics owns fd2, including in debug mode. An OS pipe avoids the
// os/exec copier wait when a daemonized host-agent keeps stderr open.
func runWithDiagnostics(cmd *exec.Cmd) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("capture limactl stderr: %w", err)
	}
	var tail diagnosticTail
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&tail, pr); close(done) }()
	cmd.Stderr = pw
	err = cmd.Run()
	_ = pw.Close()
	select {
	case <-done:
	case <-time.After(stderrDrainGrace):
	}
	_ = pr.Close()
	<-done
	raw := tail.bytes()
	operation := "limactl"
	if len(cmd.Args) > 1 {
		operation += " " + cmd.Args[1]
	}
	err = recordDiagnostics(operation, raw, err)
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
