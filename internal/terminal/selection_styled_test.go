package terminal

import (
	"image/color"
	"strings"
	"testing"
)

// The styled selection keeps each Kitty
// placeholder cell's 24-bit foreground (its image ID) in selection order,
// unwraps soft wraps and keeps hard breaks, like SelectedText.
func TestSelectedStyledTextKeepsPlaceholderColours(t *testing.T) {
	const ph = "\U0010EEEE"
	marks := []string{"̅", "̍", "̎"} // row/column diacritics 0, 1, 2
	cell := func(r, c int) string { return ph + marks[r] + marks[c] }
	idA := "\x1b[38;2;1;2;3m"
	idB := "\x1b[38;2;4;5;6m"
	// A: 3 cells soft-wrapping at 5 columns after "ab"; hard break; then B.
	text := "ab" + idA + cell(0, 0) + cell(0, 1) + cell(0, 2) + "\x1b[0mcd\r\n" + idB + cell(0, 0) + cell(0, 1) + "\x1b[0m"
	v := selectionTerminal(t, 4, 5, text)
	selectAt(t, v, MousePress, 0, 0, 0)
	selectAt(t, v, MouseMotion, 1, 2, 1)
	selectAt(t, v, MouseRelease, 1, 2, 2)
	plain, err := v.SelectedText(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	styled, err := v.SelectedStyledText(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(plain, "\n") != strings.Count(styled, "\n") {
		t.Fatalf("hard breaks differ: plain %q styled %q", plain, styled)
	}
	for _, want := range []string{"38;2;1;2;3", "38;2;4;5;6", cell(0, 0), cell(0, 2)} {
		if !strings.Contains(styled, want) {
			t.Fatalf("styled selection lacks %q: %q", want, styled)
		}
	}
	a := strings.Index(styled, "38;2;1;2;3")
	b := strings.Index(styled, "38;2;4;5;6")
	if a < 0 || b < a || strings.Index(styled, "\n") < a || strings.Index(styled, "\n") > b {
		t.Fatalf("order not preserved: %q", styled)
	}
	for _, forbidden := range []string{"\x1b]", "\x1b[?", "\x1bP"} {
		if strings.Contains(styled, forbidden) {
			t.Fatalf("styled selection contains %q", forbidden)
		}
	}
	if _, err := v.SelectedStyledText(4); err == nil {
		t.Fatal("oversized styled selection silently truncated")
	}
}

// Known default colours (host defaults or an application override) never
// reach the styled selection as OSC headers: it stays text and SGR only.
func TestSelectedStyledTextOmitsDefaultColourHeaders(t *testing.T) {
	for name, setup := range map[string]func(*Terminal){
		"host defaults": func(v *Terminal) {
			v.SetColor(0, color.RGBA{1, 2, 3, 255})
			v.SetColor(1, color.RGBA{4, 5, 6, 255})
		},
		"application override": func(v *Terminal) {
			_, _ = v.Write([]byte("\x1b]10;rgb:07/08/09\x1b\\\x1b]11;rgb:0a/0b/0c\x1b\\\x1b]4;1;rgb:11/22/33\x1b\\"))
		},
	} {
		v := selectionTerminal(t, 3, 20, "")
		setup(v)
		_, _ = v.Write([]byte("hello \x1b[38;2;1;2;3m\U0010EEEE\u0305\u0305\x1b[0m\x1b[31m red"))
		selectAt(t, v, MousePress, 0, 0, 0)
		selectAt(t, v, MouseMotion, 12, 0, 1)
		selectAt(t, v, MouseRelease, 12, 0, 2)
		styled, err := v.SelectedStyledText(1 << 20)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(styled, "\x1b]") || !strings.HasPrefix(stripSGR(styled), "hello ") || !strings.Contains(styled, "38;2;1;2;3") {
			t.Fatalf("%s: styled selection %q", name, styled)
		}
	}
}

// stripSGR removes CSI ... m sequences.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
