package markdownview

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// isPrepend reports whether r has Grapheme_Cluster_Break=Prepend under the
// segmenter of the pinned x/ansi: such a rune joins the letter after it.
func isPrepend(r rune) bool {
	joined := string(r) + "a"
	cluster, _ := ansi.FirstGraphemeCluster(joined, ansi.GraphemeWidth)
	return cluster == joined
}

// joinsPrevious reports whether r extends the character before it into one
// grapheme yet measures at least one cell alone, as a spacing mark such as
// U+0DDA does.
func joinsPrevious(r rune) bool {
	joined := "a" + string(r)
	cluster, _ := ansi.FirstGraphemeCluster(joined, ansi.GraphemeWidth)
	return cluster == joined && ansi.GraphemeWidth.StringWidth(string(r)) > 0
}

// knownWidthLimit reports whether text holds a character of the documented
// grapheme-width limit, which FuzzTolerantMarkdown skips.
func knownWidthLimit(text string) bool {
	return strings.ContainsFunc(text, func(r rune) bool { return isPrepend(r) || joinsPrevious(r) })
}

// The documented grapheme-width limit: Layout measures each styled run
// alone, but a Prepend character ending a run (here before the row's padding)
// or a spacing mark beginning one joins the neighbouring character into one
// grapheme when the row is painted. Each case paints a row one cell short;
// when a fix lands, this test fails and the limit leaves the KB.
func TestGraphemeWidthLimits(t *testing.T) {
	options := documentOptions()
	for _, text := range []string{"\u0601", "x \u06DD", "\u0D4E", "```\n\u0DDA\n```", "> \u0DDA", "a **\u0DDA**"} {
		if !knownWidthLimit(text) {
			t.Fatalf("%q is not a known limit", text)
		}
		short := false
		for _, row := range Layout(Parse(text, options), 20, LayoutOptions{Method: options.Method, Theme: options.Theme}).Rows {
			line := ansi.Strip(Paint(options.Method, row.Runs, 20, options.Theme.Surface, nil, spaceview.Style{}))
			short = short || options.Method.StringWidth(line) == 19
		}
		if !short {
			t.Fatalf("%q no longer paints a row one cell short: update the KB limit", text)
		}
	}
	for _, r := range []rune{'a', ' ', 0x0301, 0x200D, 0x00AD} {
		if isPrepend(r) || joinsPrevious(r) {
			t.Fatalf("U+%04X matched a limit", r)
		}
	}
}

// Prepended concatenation marks such as the Arabic year sign (U+0601), the
// end of ayah (U+06DD) and the Syriac abbreviation mark (U+070F) are visible
// text: Parse and copy keep them. Their width with the next character is a
// documented limit of the pinned x/ansi, not changed here.
func TestPrependMarksArePreserved(t *testing.T) {
	for _, r := range []rune{0x0601, 0x06DD, 0x070F} {
		if !isPrepend(r) {
			t.Fatalf("U+%04X is not Prepend", r)
		}
		text := "x " + string(r) + "12 y"
		if Plain(text) != text {
			t.Fatalf("Plain changed U+%04X: %q", r, Plain(text))
		}
		for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
			options := documentOptions()
			options.Method = wm
			doc := Parse(text, options)
			found := false
			for _, line := range doc.Lines {
				if strings.ContainsRune(line.Text, r) {
					found = true
					if got := line.CopyText(0, len(line.Text)); got != text {
						t.Fatalf("U+%04X copy %q, want %q", r, got, text)
					}
				}
			}
			if !found {
				t.Fatalf("U+%04X missing after Parse with method %v", r, wm)
			}
		}
	}
}
