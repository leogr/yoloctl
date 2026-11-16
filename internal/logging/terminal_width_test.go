//go:build linux || darwin

package logging

import (
	"bytes"
	"os"
	"testing"

	"github.com/creack/pty"
)

func TestTerminalWidthReadsColumnsAfterResize(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	width := terminalWidth(slave)
	for _, cols := range []uint16{80, 37, 120, 0} {
		if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: cols}); err != nil {
			t.Fatal(err)
		}
		if got := width(); got != int(cols) {
			t.Fatalf("columns=%d got=%d", cols, got)
		}
	}
	if err := slave.Close(); err != nil {
		t.Fatal(err)
	}
	if got := width(); got != 0 {
		t.Fatalf("closed terminal width=%d", got)
	}
}

func TestTerminalWidthRefusesNonTerminalWriters(t *testing.T) {
	if got := terminalWidth(&bytes.Buffer{})(); got != 0 {
		t.Fatalf("buffer width=%d", got)
	}
	file := t.TempDir() + "/log"
	// A descriptor alone is not proof that the destination is a terminal.
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if got := terminalWidth(f)(); got != 0 {
		t.Fatalf("file width=%d", got)
	}
}
