package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog/stepview"
)

// infraStderr is where a VM-side controller-internal command's stderr goes.
// While the interactive steps block is on the process terminal, that stderr
// must not be written raw: a raw line would land glued to the block and push
// it out of place. Each yoloctl record line (a level prefix and a yoloctl
// actor, the child's own log format) is shown again through the process
// Logger, in the pretty format above the block; any other line is written
// unchanged above the block. Without a block, errw is returned unchanged.
// flush writes a final line that has no newline.
func infraStderr(errw io.Writer) (w io.Writer, flush func()) {
	view := stepview.Current()
	if view == nil || errw != io.Writer(os.Stderr) {
		return errw, func() {}
	}
	l := newInfraLines(view.Writer(os.Stderr))
	return l, l.flush
}

// newInfraLines shows record lines through the process Logger and writes
// other lines to raw, one complete line at a time.
func newInfraLines(raw io.Writer) *infraLines { return &infraLines{raw: raw} }

type infraLines struct {
	mu      sync.Mutex
	raw     io.Writer
	pending []byte
}

func (l *infraLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = append(l.pending, p...)
	for {
		i := bytes.IndexByte(l.pending, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(l.pending[:i])
		l.pending = l.pending[i+1:]
		l.emit(line)
	}
}

func (l *infraLines) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pending) > 0 {
		l.emit(string(l.pending))
		l.pending = nil
	}
}

func (l *infraLines) emit(line string) {
	line = strings.TrimRight(line, "\r")
	framed := line
	if first, rest, ok := strings.Cut(line, " "); ok {
		if _, err := time.Parse(time.RFC3339Nano, first); err == nil {
			framed = rest
		}
	}
	if ownedRecordFrame(framed) {
		record := spaceLogRecord(line)
		record.Time = time.Now()
		_ = logging.L().Handler().Handle(context.Background(), record)
		return
	}
	_, _ = l.raw.Write([]byte(line + "\n"))
}
