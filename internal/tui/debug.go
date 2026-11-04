package tui

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var debugLogMu sync.Mutex

func debugLogf(format string, args ...any) {
	if !debugTUI {
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
	_, _ = fmt.Fprintf(f, "%s tui: "+format+"\n", append([]any{time.Now().Format(time.RFC3339Nano)}, args...)...)
}
