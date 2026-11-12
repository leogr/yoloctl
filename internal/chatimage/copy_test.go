package chatimage

import (
	"image/color"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/terminal"
)

const ph = "\U0010EEEE"

var marks = []string{"̅", "̍", "̎"}

func cell(r, c int) string { return ph + marks[r] + marks[c] }

func rgb(id uint32) string {
	return "\x1b[38;2;" + itoa(id>>16) + ";" + itoa(id>>8&0xff) + ";" + itoa(id&0xff) + "m"
}

func itoa(n uint32) string { return strconv.FormatUint(uint64(n), 10) }

func TestMapSelectionReplacesImageRunsWithChips(t *testing.T) {
	l := NewLedger()
	l.Remember(0x010203, "[file cat.png · 12 KiB]")
	for _, c := range []struct {
		name, styled, want string
	}{
		{"text only", "hello\r\nworld", "hello\nworld"},
		{"one image between text", "a " + rgb(0x010203) + cell(0, 0) + cell(0, 1) + "\x1b[0m b", "a [file cat.png · 12 KiB] b"},
		{"rows of one image copy once", "x\r\n" + rgb(0x010203) + cell(0, 0) + cell(0, 1) + "\r\n" + cell(1, 0) + cell(1, 1) + "\x1b[0m\r\ny",
			"x\n[file cat.png · 12 KiB]\ny"},
		{"unknown id is generic", rgb(0x0a0b0c) + cell(0, 0) + "\x1b[0m", GenericChip},
		{"palette colour is generic", "\x1b[38;5;196m" + cell(0, 0), GenericChip},
		{"no colour is generic", cell(0, 0), GenericChip},
		{"generic run is one chip", "a " + cell(0, 0) + cell(0, 1) + cell(0, 2) + " b", "a " + GenericChip + " b"},
		{"palette run is one chip", "\x1b[38;5;196m" + cell(0, 0) + cell(0, 1) + "\x1b[38;5;21m" + cell(0, 2), GenericChip},
		{"generic run then known image", cell(0, 0) + cell(0, 1) + rgb(0x010203) + cell(0, 0), GenericChip + "[file cat.png · 12 KiB]"},
		{"two images on one line", rgb(0x010203) + cell(0, 0) + rgb(0x0a0b0c) + cell(0, 0), "[file cat.png · 12 KiB]" + GenericChip},
		{"same image again after text", rgb(0x010203) + cell(0, 0) + "\x1b[0m-" + rgb(0x010203) + cell(0, 1),
			"[file cat.png · 12 KiB]-[file cat.png · 12 KiB]"},
		{"colon form", "\x1b[38:2::1:2:3m" + cell(0, 0), "[file cat.png · 12 KiB]"},
		{"bold and background kept apart", "\x1b[1;48;2;9;9;9;38;2;1;2;3m" + cell(0, 0), "[file cat.png · 12 KiB]"},
		{"blank lines kept", "a\r\n\r\nb", "a\n\nb"},
	} {
		got, err := MapSelection(c.styled, l)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for _, bad := range []string{"\x1b]52;c;eA==\x07", "\x1b[?1049h", "\x1bPq", "a\x07b", "\x1b[38;2;300;0;0m"} {
		if _, err := MapSelection(bad, l); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLedgerIsBounded(t *testing.T) {
	l := NewLedger()
	for id := uint32(1); id <= MaxLedgerEntries+10; id++ {
		l.Remember(id, "chip")
	}
	if got := l.Chip(1); got != GenericChip {
		t.Fatalf("oldest id kept: %q", got)
	}
	if got := l.Chip(MaxLedgerEntries + 10); got != "chip" {
		t.Fatalf("newest id lost: %q", got)
	}
	l.Remember(7001, strings.Repeat("x", 511)+"😀")
	if got := l.Chip(7001); len(got) > maxChipBytes || got != strings.Repeat("x", 511) {
		t.Fatalf("multibyte boundary: %d bytes", len(got))
	}
	l.Remember(7000, "a\x1b[31m\nb"+ph)
	if got := l.Chip(7000); strings.ContainsAny(got, "\x1b\n") || strings.Contains(got, ph) {
		t.Fatalf("chip not sanitized: %q", got)
	}
}

// The real native path: select two previews (A, B) and text across a soft
// wrap and hard breaks, push them into history, then copy.
func TestMapSelectionOnNativeStyledSelection(t *testing.T) {
	v, err := terminal.New(5, 6, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()
	// Known default colours (host and application) must not break the copy.
	v.SetColor(0, color.RGBA{1, 2, 3, 255})
	v.SetColor(1, color.RGBA{4, 5, 6, 255})
	_, _ = v.Write([]byte("\x1b]10;rgb:07/08/09\x1b\\"))
	a, b := uint32(0x112233), uint32(0x445566)
	text := "see " + rgb(a) + cell(0, 0) + cell(0, 1) + cell(0, 2) + "\x1b[0m!\r\n" +
		rgb(a) + cell(1, 0) + cell(1, 1) + cell(1, 2) + "\x1b[0m\r\n" +
		"then " + rgb(b) + cell(0, 0) + "\x1b[0m\r\nz"
	if _, err := v.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		action terminal.MouseAction
		x, y   int
	}{{terminal.MousePress, 0, 0}, {terminal.MouseMotion, 0, 4}, {terminal.MouseRelease, 0, 4}} {
		if err := v.Select(step.action, step.x, step.y, false, 0, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.Write([]byte(strings.Repeat("\r\nfiller", 8))); err != nil {
		t.Fatal(err)
	}
	styled, err := v.SelectedStyledText(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLedger()
	l.Remember(a, "[file a.png · 1 KiB]")
	l.Remember(b, "[file b.png · 2 KiB]")
	got, err := MapSelection(styled, l)
	if err != nil {
		t.Fatal(err)
	}
	want := "see [file a.png · 1 KiB]!\n[file a.png · 1 KiB]\nthen [file b.png · 2 KiB]"
	if got != want {
		t.Fatalf("got %q\nwant %q\nstyled %q", got, want, styled)
	}
	plain, _ := v.SelectedText(1 << 20)
	if strings.Count(plain, "\n") != strings.Count(got, "\n") {
		t.Fatalf("line structure differs from plain selection %q", plain)
	}
}
