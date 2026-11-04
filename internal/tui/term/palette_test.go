package term

import (
	"bytes"
	"image/color"
	"strings"
	"testing"
)

func TestPaletteQueriesWaitForMeasuredHostColor(t *testing.T) {
	for _, end := range []string{"\a", "\x1b\\"} {
		query := "\x1b]4;1;?;255;?" + end
		for split := 0; split <= len(query); split++ {
			var replies bytes.Buffer
			e := newTestEmulator(t, 2, 20, &replies, 20)
			feed(t, e, query[:split])
			feed(t, e, query[split:])
			if replies.Len() != 0 {
				t.Fatalf("invented host palette: %q", replies.String())
			}
			// A host response during a fragmented child CSI must not replace
			// or complete that child sequence with an injected color query.
			feed(t, e, "\x1b[3")
			e.SetPaletteColor(1, color.RGBA{1, 2, 3, 255})
			feed(t, e, "1mred")
			if !strings.Contains(e.RenderLine(0), "\x1b[38;5;1m") {
				t.Fatal("palette update corrupted the unfinished child control")
			}
			if got := replies.String(); got != "\x1b]4;1;rgb:0101/0202/0303"+end {
				t.Fatalf("deferred reply: %q", got)
			}
			replies.Reset()
			feed(t, e, "\x1b]4;255;#abcdef\a\x1b]4;255;?"+end)
			if got := replies.String(); got != "\x1b]4;255;rgb:abab/cdcd/efef"+end {
				t.Fatalf("known child override: %q", got)
			}
			replies.Reset()
			e.SetPaletteColor(255, color.Black)
			if replies.Len() != 0 {
				t.Fatalf("duplicate pending reply: %q", replies.String())
			}
			feed(t, e, "\x1b]104;255\a\x1b]4;255;?"+end)
			if got := replies.String(); got != "\x1b]4;255;rgb:0000/0000/0000"+end {
				t.Fatalf("reset host default: %q", got)
			}
		}
	}
}

func TestPaletteClearDropsOldPendingQueries(t *testing.T) {
	var replies bytes.Buffer
	e := newTestEmulator(t, 2, 20, &replies, 20)
	for range 100 {
		feed(t, e, "\x1b]4;42;?\a")
	}
	e.Clear()
	e.SetPaletteColor(42, color.White)
	if replies.Len() != 0 {
		t.Fatalf("answered a previous command's query: %q", replies.String())
	}
	feed(t, e, "\x1b]4;42;?\a")
	if got := replies.String(); got != "\x1b]4;42;rgb:ffff/ffff/ffff\a" {
		t.Fatalf("next command lost the measured palette: %q", got)
	}
}
