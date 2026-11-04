package term

import (
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/termio"
)

func TestConsoleErrorIsTextAndPreservesRunningCommand(t *testing.T) {
	c := NewConsole(8, 40, 20, nil)
	t.Cleanup(func() { _ = c.Close() })
	effects := 0
	c.SetOSC52Handler(func(termio.OSC52) { effects++ })
	c.SetBellHandler(func(n int) { effects += n })
	if !c.ShowError("before\a\x1b]52;c;eA==\a\x1b[2J\nafter") {
		t.Fatal("idle console rejected diagnostic")
	}
	if got := c.GridText(); !strings.Contains(got, "before") || !strings.Contains(got, "after") || effects != 0 {
		t.Fatalf("diagnostic executed controls or lost text: %q, effects=%d", got, effects)
	}
	if started, err := c.Run(nil, "$ live command", "sleep", "30"); !started || err != nil {
		t.Fatalf("start: %t, %v", started, err)
	}
	before := c.GridText()
	if c.ShowError("late failure") || !c.Busy() || c.GridText() != before {
		t.Fatal("diagnostic overwrote the active command")
	}
}

func TestConsoleCloseJoinsCommandAndReleasesCore(t *testing.T) {
	c := NewConsole(4, 40, 20, nil)
	t.Cleanup(func() { _ = c.Close() })
	if started, err := c.Run(nil, "", "sh", "-c", "sleep 30"); err != nil || !started {
		t.Fatalf("start: %t, %v", started, err)
	}
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close did not join the PTY workers")
	}
	if c.Busy() {
		t.Fatal("closed console still running")
	}
	if started, err := c.Run(nil, "", "true"); started || err == nil {
		t.Fatal("closed console accepted another command")
	}
	if c.vt.Err() == nil {
		t.Fatal("core was not released")
	}
}
