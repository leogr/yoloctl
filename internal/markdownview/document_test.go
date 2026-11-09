package markdownview

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func documentOptions() ParseOptions {
	return ParseOptions{Method: ansi.GraphemeWidth, Theme: spaceview.NewTheme(true, nil, nil)}
}

func TestPlainConsumerUsesTheSharedRendererWithoutChatResolution(t *testing.T) {
	options := documentOptions()
	text := "# Report\n@reader m:ERERESIiQzOERFVVVVVVVQ and [doc](https://example.org)\n| A | B |\n| --- | --- |\n| x | y |\n```go\nprintln(\"hi\")\n```"
	doc := Parse(text, options)
	if doc.Sources != nil {
		t.Fatal("ordinary parse eagerly materialized source mappings")
	}
	if !strings.Contains(RenderText(doc), "@reader m:ERERESIiQzOERFVVVVVVVQ") {
		t.Fatal("plain consumer changed context tokens")
	}
	url, table := false, false
	for _, line := range doc.Lines {
		table = table || line.Table != nil
		for _, piece := range line.Pieces {
			if piece.Act.Kind != IntentNone && piece.Act.Kind != IntentURL {
				t.Fatal("plain consumer resolved a caller-specific action", piece.Act)
			}
			url = url || piece.Act == (Action{Kind: IntentURL, Value: "https://example.org"})
		}
	}
	// Individual lexer classes are checked by the moved lexer corpus; here the
	// important boundary is that actual code/table/URL data shares one layout.
	if !url || !table {
		t.Fatal("generic parse did not preserve table/URL content")
	}
	layout := Layout(doc, 32, LayoutOptions{Method: options.Method, Theme: options.Theme})
	copyCode := false
	for _, row := range layout.Rows {
		painted := Paint(options.Method, row.Runs, 32, options.Theme.Surface, nil, spaceview.Style{})
		if options.Method.StringWidth(painted) != 32 {
			t.Fatal("shared painter width changed")
		}
		for _, run := range row.Runs {
			copyCode = copyCode || run.Act == (Action{Kind: IntentCopyCode, Value: "println(\"hi\")\n"})
		}
	}
	if layout.Limited || !copyCode {
		t.Fatal("layout lost the inert code-copy region", layout.Limited, copyCode)
	}
}

func TestSourceMapsAreExplicitAndUseTheOriginalSelectionAlgorithm(t *testing.T) {
	options := documentOptions()
	options.MapSource = true
	doc := Parse("intro **bold** end", options)
	if len(doc.Sources) != len(doc.Lines) || doc.Sources[0].Src == nil {
		t.Fatal("explicit source mapping unavailable")
	}
	selection := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: "intro **bold** end", Render: func(s string) string {
		return RenderText(Parse(s, options))
	}}
	start := strings.Index(doc.Lines[0].Text, "bold")
	if got, ok := selection.Exact(0, start, 0, start+4, true, true); !ok || got != "**bold**" {
		t.Fatal("whole formatted source selection changed", got, ok)
	}
}

func TestGenericReplacementPreservesWholeCopyAndPartialSourceRefusal(t *testing.T) {
	options := documentOptions()
	options.MapSource = true
	text := "before token after"
	// Like chat's reference decorator, a caller marks the replacement span
	// before changing its display text. The shared helper owns no resolver.
	options.Decorate = func(text string, pieces []Piece, theme spaceview.Theme, prose bool) []Piece {
		if text == "before token after" {
			return Overlay(pieces, 7, 12, func(p Piece) Piece {
				p.Act = Action{Kind: 42, Value: "caller target"}
				return p
			})
		}
		return pieces
	}
	doc := Parse(text, options)
	lines := ApplyReplacements(doc.Lines, [][]Replacement{{{Start: 7, End: 12, Text: "expanded preview", Act: Action{Kind: 42, Value: "caller target"}}}})
	line := lines[0]
	if line.CopyText(0, len(line.Text)) != text || line.Pieces[1].Act.Kind != 42 {
		t.Fatal("replacement changed whole copy or interpreted opaque action")
	}
	lo, hi := ReplacementBounds(line, 8, 10)
	if lo != 7 || hi != 23 {
		t.Fatal("replacement hit range did not remain whole", lo, hi)
	}
	sources := append([]LineSource(nil), doc.Sources...)
	sources[0].Src = ReplacementSourceMap(line).Then(sources[0].Src)
	selection := SourceSelection{Lines: lines, Sources: sources, Text: text, Render: func(s string) string {
		return RenderText(Parse(s, options))
	}}
	if got, ok := selection.Exact(0, 0, 0, len(line.Text), true, true); !ok || got != text {
		t.Fatal("whole replacement source changed", got, ok)
	}
	if got, ok := selection.Exact(0, 8, 0, 10, true, true); ok {
		t.Fatal("partial replacement unexpectedly became exact source", got)
	}
}

func TestLayoutReportsTheExistingRowLimit(t *testing.T) {
	options := documentOptions()
	doc := Parse(strings.Repeat("x\n", MaxBodyRows+2), options)
	layout := Layout(doc, 20, LayoutOptions{Method: options.Method, Theme: options.Theme})
	last := layout.Rows[len(layout.Rows)-1]
	if !layout.Limited || len(layout.Rows) != MaxBodyRows+1 || !last.Decor || !strings.Contains(last.Runs[0].Text, "display row limit") {
		t.Fatal("existing limit marker/metadata changed", layout.Limited, len(layout.Rows))
	}
}
