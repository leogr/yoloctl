package terminal

import (
	"bytes"
	"image/color"
	"strings"
	"testing"
)

func TestPaletteRenderingPreservesHostIndices(t *testing.T) {
	vt := newTestTerminal(t, 2, 20, 20, nil)
	_, _ = vt.Write([]byte("\x1b[31;44;58;5;9;4mcolor"))
	lines, err := vt.Lines(false, false, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\x1b[38;5;1m", "\x1b[48;5;4m", "\x1b[58;5;9m"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("host palette reference %q lost: %q", want, lines[0])
		}
	}
	_, _ = vt.Write([]byte("\x1b]4;1;#123456\a"))
	lines, _ = vt.Lines(false, false, 0, 1)
	if !strings.Contains(lines[0], "\x1b[38;2;18;52;86m") || !strings.Contains(lines[0], "\x1b[48;5;4m") {
		t.Fatalf("child override must be local and leave other indices intact: %q", lines[0])
	}
	_, _ = vt.Write([]byte("\x1b]104;1\a"))
	lines, _ = vt.Lines(false, false, 0, 1)
	if !strings.Contains(lines[0], "\x1b[38;5;1m") {
		t.Fatalf("reset must restore host index: %q", lines[0])
	}
}

func TestPaletteDefaultsOverridesHistoryAndReset(t *testing.T) {
	var replies bytes.Buffer
	vt := newTestTerminal(t, 2, 20, 20, &replies)
	for _, index := range []int{0, 1, 15, 16, 255} {
		if err := vt.SetPaletteColor(index, color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 255}); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = vt.Write([]byte("\x1b[31mold\r\nnow\r\nlast"))
	_, _ = vt.Write([]byte("\x1b]4;1;#abcdef\x1b\\"))
	if err := vt.SetPaletteColor(1, color.RGBA{R: 0x65, G: 0x43, B: 0x21, A: 255}); err != nil {
		t.Fatal(err)
	}
	for _, history := range []bool{false, true} {
		lines, _ := vt.Lines(history, false, 0, 1)
		if !strings.Contains(lines[0], "\x1b[38;2;171;205;239m") {
			t.Fatalf("host update clobbered child color (history=%v): %q", history, lines)
		}
	}
	_, _ = vt.Write([]byte("\x1b]104;1\a\x1b]4;1;?\a"))
	if got := replies.String(); got != "\x1b]4;1;rgb:6565/4343/2121\a" {
		t.Fatalf("reset did not restore measured host color: %q", got)
	}
	replies.Reset()
	_, _ = vt.Write([]byte("\x1bc\x1b]4;0;?;255;?\x1b\\"))
	if got := replies.String(); got != "\x1b]4;0;rgb:1212/3434/5656\x1b\\\x1b]4;255;rgb:1212/3434/5656\x1b\\" {
		t.Fatalf("RIS lost host defaults: %q", got)
	}
	other := newTestTerminal(t, 2, 20, 20, nil)
	_, _ = other.Write([]byte("\x1b[31muntouched"))
	lines, _ := other.Lines(false, false, 0, 1)
	if strings.Contains(lines[0], "38;2;") {
		t.Fatalf("another tab inherited the child override: %q", lines)
	}
}
