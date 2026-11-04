package terminal

import (
	"strings"
	"testing"
)

func TestInputReplayBoundAndRecovery(t *testing.T) {
	vt, err := NewInputTracker()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	// DCS passthrough can remain open for arbitrarily many PTY reads. Export
	// must never silently truncate its parser continuation or grow without bound.
	if _, err := vt.Write([]byte("\x1b[>3u\x1bPq" + strings.Repeat("x", 33*1024))); err != nil {
		t.Fatal(err)
	}
	if _, err := vt.InputReplay(); err == nil {
		t.Fatal("oversized continuation exported")
	}
	if _, err := vt.Write([]byte("\x1b\\")); err != nil {
		t.Fatal(err)
	}
	p, err := vt.InputReplay()
	if err != nil {
		t.Fatal(err)
	}
	restored := newTestTerminal(t, 3, 20, 0, nil)
	if _, err := restored.Write(p); err != nil {
		t.Fatal(err)
	}
	if restored.Get(8) != 3 {
		t.Fatal("completed string lost keyboard flags")
	}
}
