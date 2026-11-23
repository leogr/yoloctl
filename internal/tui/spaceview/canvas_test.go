package spaceview

import "testing"

// The escape bytes every native view writes for a cell style: rendition flags
// first, then foreground and background, joined by ';'. Out-of-range values
// keep their historical decimal encoding.
func TestStyleSGRBytes(t *testing.T) {
	for _, tc := range []struct {
		style Style
		want  string
	}{
		{Style{}, ""},
		{Style{Bold: true}, "\x1b[1m"},
		{Style{Italic: true}, "\x1b[3m"},
		{Style{Underline: true}, "\x1b[4m"},
		{Style{Bold: true, Italic: true, Underline: true}, "\x1b[1;3;4m"},
		{Style{FG: ix(0)}, "\x1b[38;5;0m"},
		{Style{FG: ix(255)}, "\x1b[38;5;255m"},
		{Style{BG: ix(16)}, "\x1b[48;5;16m"},
		{Style{FG: rgb(1, 2, 3)}, "\x1b[38;2;1;2;3m"},
		{Style{FG: rgb(255, 255, 255), BG: rgb(0, 0, 0)}, "\x1b[38;2;255;255;255;48;2;0;0;0m"},
		{Style{FG: ix(7), BG: rgb(200, 100, 50), Bold: true, Underline: true}, "\x1b[1;4;38;5;7;48;2;200;100;50m"},
		{Style{FG: -1 << 25}, "\x1b[38;5;-33554433m"},
		{Style{BG: 1 << 25}, "\x1b[48;5;33554431m"},
	} {
		if got := tc.style.sgr(); got != tc.want {
			t.Errorf("%+v: sgr() = %q, want %q", tc.style, got, tc.want)
		}
	}
	if got, want := StyleText("x", Style{}), "x\x1b[0m"; got != want {
		t.Errorf("StyleText default = %q, want %q", got, want)
	}
	if got, want := StyleText("x", Style{FG: ix(244), Italic: true}), "\x1b[3;38;5;244mx\x1b[0m"; got != want {
		t.Errorf("StyleText = %q, want %q", got, want)
	}
}
