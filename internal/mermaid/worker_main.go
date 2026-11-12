package mermaid

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// WorkerMain is the hidden `yoloctl __mermaid-worker` process: framed
// requests on stdin, framed replies on stdout, until stdin closes (the parent
// ended). It initializes no CLI configuration, controller context or
// terminal; its only input is the source text it is sent.
// It refuses (exit 2) outside the host CLI context, before any setup.
func WorkerMain() int {
	if !onHost() {
		_, _ = os.Stderr.WriteString("mermaid worker: host only\n")
		return 2
	}
	signal.Ignore(syscall.SIGINT) // the parent owns Ctrl-C; it kills the group
	if err := RunWorker(context.Background(), os.Stdin, os.Stdout); err != nil {
		return 1
	}
	return 0
}
