package term

import (
	"bytes"
	"testing"
)

func TestFocusReportsOnlyWhenRequestedAndChanged(t *testing.T) {
	var input bytes.Buffer
	e := newTestEmulator(t, 2, 12, &input, 10)
	for _, focused := range []bool{true, false, true} {
		if err := e.UpdateFocus(focused); err != nil {
			t.Fatal(err)
		}
	}
	if input.Len() != 0 {
		t.Fatal("focus reached an application without mode 1004")
	}
	feed(t, e, "\x1b[?1004h")
	for _, focused := range []bool{true, true, false, false, true} {
		if err := e.UpdateFocus(focused); err != nil {
			t.Fatal(err)
		}
	}
	if got := input.String(); got != "\x1b[I\x1b[O\x1b[I" {
		t.Fatalf("focus transcript = %q", got)
	}
	feed(t, e, "\x1bc")
	_ = e.UpdateFocus(false)
	if input.Len() != 9 {
		t.Fatal("RIS did not disable focus reports")
	}
}
