package termio_test

import (
	"fmt"
	"testing"

	"github.com/leogr/yoloctl/internal/terminal"
	"github.com/leogr/yoloctl/internal/termio"
)

func TestSessionEndResetsEveryMouseProtocolAndShiftCapture(t *testing.T) {
	vt, err := terminal.New(4, 20, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vt.Close() }()
	modes := []uint16{9, 1000, 1002, 1003, 1005, 1006, 1007, 1015, 1016}
	for _, mode := range modes {
		_, _ = vt.Write(fmt.Appendf(nil, "\x1b[?%dh", mode))
	}
	_, _ = vt.Write([]byte("\x1b[>1s"))
	_, _ = vt.Write([]byte(termio.SessionEndResetHost))
	for _, mode := range modes {
		if vt.Mode(mode) {
			t.Errorf("mouse mode %d left enabled", mode)
		}
	}
	if got := vt.MouseMode(); got.Tracking != 0 || got.Format != 0 || got.ShiftCapture != terminal.MouseShiftSelection {
		t.Fatalf("mouse state left on host: %+v", got)
	}
}
