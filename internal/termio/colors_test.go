package termio

import (
	"image/color"
	"strings"
	"testing"
)

func TestPaletteReports(t *testing.T) {
	for _, intro := range []string{"\x1b]", "\x9d"} {
		for _, end := range []string{"\a", "\x1b\\", "\x9c"} {
			entries, ok := ParsePaletteReport(intro + "4;0;rgb:f/8/0;255;rgb:1234/abcd/ef00" + end)
			if !ok || len(entries) != 2 || entries[0] != (PaletteEntry{0, color.RGBA{255, 136, 0, 255}}) || entries[1] != (PaletteEntry{255, color.RGBA{18, 171, 239, 255}}) {
				t.Fatalf("palette reply: %+v, %v", entries, ok)
			}
		}
	}
	for _, sequence := range []string{
		"\x1b]4;0;rgb:ff/00/00", "\x1b]11;rgb:ff/00/00\a",
		"\x1b]4;256;rgb:ff/00/00\a", "\x1b]4;-1;rgb:ff/00/00\a",
		"\x1b]4;0;rgb:gg/00/00\a", "\x1b]4;0;rgb:00000/0/0\a",
		"\x1b]4;0;rgb:+f/0/0\a", "\x1b]4;+0;rgb:f/0/0\a",
		"\x1b]4;0;?\a", "\x1b]4;0;rgb:/0/0\a",
		"\x1b]4;0;rgb:f/0/0;1;invalid\a", "\x1b]4;0;rgb:f/0/0\aextra",
		"\x1b]4;0;rgb:f/0/0\x1b]52;c;YQ==\a", strings.Repeat("x", 16*1024+1),
	} {
		if entries, ok := ParsePaletteReport(sequence); ok || entries != nil {
			t.Fatalf("accepted invalid report %q: %+v", sequence, entries)
		}
	}
}
