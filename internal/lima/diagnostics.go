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

// LogDiagnostics records private host diagnostics without writing a terminal.
// Callers log a fixed operation label, never command argv, environment or stdin.
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
	fd, err := unix.Openat(dir, filepath.Base(path), unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("open host diagnostics log %q: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0777 != 0600 || st.Nlink != 1 {
		return fmt.Errorf("host diagnostics log %q must be an owned private regular file", path)
	}
	// Quote diagnostic bytes: explicitly reading the log cannot replay controls.
	line := time.Now().UTC().Format(time.RFC3339Nano) + " " + strconv.Quote(operation) + " " + strconv.QuoteToASCII(string(raw)) + "\n"
	if _, err := io.WriteString(f, line); err != nil {
		return err
	}
	return errors.Join(f.Sync(), unix.Fsync(dir))
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
	if logErr := LogDiagnostics(operation, raw); logErr != nil {
		if err == nil {
			err = fmt.Errorf("limactl completed but recording its diagnostics failed: %w", logErr)
		} else {
			err = errors.Join(err, logErr)
		}
	}
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
