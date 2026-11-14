package markdownview

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// mapped parses text with its source map, at both width methods.
func mapped(t *testing.T, text string, f func(wm ansi.Method, doc Document)) {
	t.Helper()
	for _, wm := range []ansi.Method{ansi.WcWidth, ansi.GraphemeWidth} {
		options := documentOptions()
		options.Method, options.MapSource = wm, true
		f(wm, Parse(text, options))
	}
}

// B3: a heading's closing sequence of unescaped '#' (after a space or TAB,
// with only spaces or TABs after it) is dropped; a whole-line Quote still
// takes the source line through its end.
func TestHeadingClosingHashes(t *testing.T) {
	for input, want := range map[string]string{
		"## T ##":     "T",
		"## T #   ":   "T",
		"## T\t###":   "T",
		"## T#":       "T#",
		"## C#":       "C#",
		"## T \\#":    "T #",
		"### T \\###": "T ###",
		"# #":         "",
		"#   ###  ":   "",
		"## a # b ##": "a # b",
	} {
		mapped(t, input, func(_ ansi.Method, doc Document) {
			dl := doc.Lines[0]
			if dl.Text != want || want != "" && (len(dl.Pieces) == 0 || !dl.Pieces[0].Style.Bold) {
				t.Fatalf("%q shows %q (bold %v), want %q", input, dl.Text, len(dl.Pieces) > 0 && dl.Pieces[0].Style.Bold, want)
			}
			if want == "" {
				return
			}
			src := doc.Sources[0]
			if _, wide, ok := LineEnd(src, len(dl.Text)); !ok || wide != len(input) {
				t.Fatalf("%q: a whole-line end maps to %d, not %d", input, wide, len(input))
			}
			dropped := map[string]bool{"## T ##": true, "## T #   ": true, "## T\t###": true, "## a # b ##": true}
			if bare, _, ok := LineEnd(src, len(dl.Text)); !ok || dropped[input] == (bare == len(input)) {
				t.Fatalf("%q: the bare end %d takes the closing sequence", input, bare)
			}
		})
	}
}
