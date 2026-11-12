package rotatelog

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

const streamChunk = 64 << 10

// Writer is a long-lived log sink, not an inherited file descriptor. Each
// append resolves the current generation, so children must write through a
// relay owned by their long-lived supervisor. At most Limit bytes are kept in
// each of the current and previous generations.
//
// Write remembers the first failure in Err and returns it to structured log
// handlers. BestEffort keeps such failures out of raw child exit status. Later
// writes still try. Close belongs to the relay owner, after all producers and
// logger flushes finish.
type Writer struct {
	mu   sync.Mutex
	dir  int
	name string
	path string
	app  Appender
	err  error
}

// Open uses an existing owned directory that is not writable by other users.
// It never follows the final directory or log name through a symlink and never
// creates a directory. Initial open/privacy errors are returned to the caller.
func Open(path string, limit int64) (*Writer, error) {
	name := filepath.Base(path)
	if limit <= 0 || name == "." || name == ".." || name == string(filepath.Separator) {
		return nil, errors.New("invalid rotating log path or limit")
	}
	dir, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	err = unix.Fstat(dir, &st)
	if err == nil && (st.Uid != uint32(os.Getuid()) || st.Mode&0o022 != 0) {
		err = fmt.Errorf("log directory %q must be owned and not writable by other users", filepath.Dir(path))
	}
	w := &Writer{dir: dir, name: name, path: path, app: Appender{Limit: limit, TrimOversize: true}}
	if err == nil {
		err = w.app.Append(dir, name, path, nil)
	}
	if err != nil {
		_ = unix.Close(dir)
		return nil, err
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dir < 0 {
		return 0, os.ErrClosed
	}
	written := 0
	for rest := p; len(rest) != 0; {
		n := min(len(rest), streamChunk)
		if int64(n) > w.app.Limit {
			n = int(w.app.Limit)
		}
		copied, err := w.app.append(w.dir, w.name, w.path, rest[:n])
		written += copied
		if err != nil {
			if w.err == nil {
				w.err = err
			}
			return written, err
		}
		rest = rest[n:]
	}
	return len(p), nil
}

func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dir < 0 {
		return nil
	}
	err := unix.Close(w.dir)
	w.dir = -1
	return err
}

// BestEffort relays raw child output without turning a logging error into a
// different process result. Err still exposes the failure to the owner.
func (w *Writer) BestEffort() io.Writer { return bestEffort{w} }

type bestEffort struct{ writer *Writer }

func (w bestEffort) Write(p []byte) (int, error) {
	_, _ = w.writer.Write(p)
	return len(p), nil
}
