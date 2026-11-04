package term

import (
	"bytes"
	"testing"

	"github.com/leogr/yoloctl/internal/terminal"
)

func TestSelectionDuringHeldOutput(t *testing.T) {
	e := newEmulator(3, 20, &bytes.Buffer{}, 100)
	t.Cleanup(func() { _ = e.Close() })
	_, _ = e.Write([]byte("hello world"))
	for _, event := range []struct {
		action terminal.MouseAction
		x      int
	}{{terminal.MousePress, 0}, {terminal.MouseMotion, 5}, {terminal.MouseRelease, 5}} {
		if err := e.SelectMouse(event.action, event.x, 0, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = e.Write([]byte("\x1b[?2026h\rchanged text"))
	if _, err := e.SelectedText(); err == nil {
		t.Fatal("copied hidden live text from held selection")
	}
	if err := e.SelectMouse(terminal.MousePress, 10, 0, false, 0); err != nil {
		t.Fatal(err)
	}
	f := e.Frame()
	if f.Selection[0].Start != 0 || f.Selection[0].End != 5 {
		t.Fatal("held frame selection was modified")
	}
	e.EndRenderHold()
	if _, err := e.SelectedText(); err != nil {
		t.Fatal(err)
	}
}
