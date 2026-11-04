package term

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"time"
)

var debugLogMu sync.Mutex

func debugEnabled() bool {
	return os.Getenv("YOLOCTL_TUI_DEBUG") == "1"
}

func debugLogf(format string, args ...any) {
	if !debugEnabled() {
		return
	}
	path := os.Getenv("YOLOCTL_TUI_DEBUG_LOG")
	if path == "" {
		path = os.TempDir() + "/yoloctl-tui-debug.log"
	}
	debugLogMu.Lock()
	defer debugLogMu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s term: "+format+"\n", append([]any{time.Now().Format(time.RFC3339Nano)}, args...)...)
}

func keyboardProtocolSeqs(p []byte) (seqs []string, tail []byte) {
	boundedTail := func(p []byte) []byte {
		if len(p) > 128 {
			p = p[len(p)-128:]
		}
		return append([]byte(nil), p...)
	}
	for {
		i := bytes.Index(p, []byte{0x1b, '['})
		if i < 0 {
			if len(p) > 1 && p[len(p)-1] == 0x1b {
				return seqs, boundedTail(p[len(p)-1:])
			}
			return seqs, nil
		}
		if i > 0 {
			p = p[i:]
		}
		if len(p) < 3 {
			return seqs, boundedTail(p)
		}
		j := 2
		for j < len(p) && (p[j] < 0x40 || p[j] > 0x7e) {
			j++
		}
		if j >= len(p) {
			return seqs, boundedTail(p)
		}
		seq := p[:j+1]
		if seq[len(seq)-1] == 'u' && len(seq) >= 3 {
			switch seq[2] {
			case '?', '>', '<', '=':
				seqs = append(seqs, fmt.Sprintf("%q", seq))
			}
		}
		p = p[j+1:]
	}
}
