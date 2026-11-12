package markdownview

import (
	"slices"
	"strings"
	"testing"
)

const diagramSource = "flowchart LR\n A --> B\n"

// drawingRows is a fake drawing: two box rows that are not the source.
var drawingRows = []string{"┌───┐  ┌───┐", "│ A │─▶│ B │", "└───┘  └───┘"}

func diagramOptions(lookups *[]string, d Diagram) ParseOptions {
	options := documentOptions()
	options.Diagrams = func(source string) (Diagram, bool) {
		*lookups = append(*lookups, source)
		return d, true
	}
	return options
}

func TestMermaidFenceShowsItsDrawingAsCodeRows(t *testing.T) {
	var lookups []string
	options := diagramOptions(&lookups, Diagram{Lines: drawingRows})
	text := "before\n```Mermaid\n" + diagramSource + "```\nafter"
	doc := Parse(text, options)
	if !slices.Equal(lookups, []string{diagramSource}) {
		t.Fatalf("lookups %q", lookups)
	}
	var got []string
	for _, l := range doc.Lines {
		got = append(got, l.Text)
	}
	if want := append(append([]string{"before", "Mermaid"}, drawingRows...), "", "after"); !slices.Equal(got, want) {
		t.Fatalf("lines %q, want %q", got, want)
	}
	open := doc.Lines[1]
	if open.Fence != FenceOpen || open.Block != diagramSource {
		t.Fatalf("the opener keeps the fence source for its copy button: %+v", open)
	}
	for _, l := range doc.Lines[2:5] {
		if !l.Code || l.Decor || l.Diagram != 1 {
			t.Fatalf("drawing row %+v", l)
		}
	}
	// Drag copy takes the drawing; it is never re-wrapped at its width.
	if !strings.Contains(RenderText(doc), strings.Join(drawingRows, "\n")) {
		t.Fatalf("copy text %q", RenderText(doc))
	}
	width := 12 + 2*CardPad(16)
	if DiagramWidth(width) != 12 {
		t.Fatalf("DiagramWidth(%d) = %d", width, DiagramWidth(width))
	}
	_, rows := painted(t, doc, options, width)
	for _, row := range drawingRows {
		if !slices.ContainsFunc(rows, func(r string) bool { return strings.Contains(r, row) }) {
			t.Fatalf("row %q wrapped or lost: %q", row, rows)
		}
	}
}

func TestMermaidFallbackKeepsCodeWithOneNoteRow(t *testing.T) {
	var lookups []string
	options := diagramOptions(&lookups, Diagram{Note: "mermaid: not drawn (parse error)"})
	text := "```mermaid\n" + diagramSource + "```\nafter"
	plain := Parse(text, documentOptions())
	doc := Parse(text, options)
	if len(doc.Lines) != len(plain.Lines)+1 {
		t.Fatalf("%d lines, want today's %d plus the note", len(doc.Lines), len(plain.Lines))
	}
	for i, l := range plain.Lines[:4] {
		if doc.Lines[i].Text != l.Text || doc.Lines[i].Code != l.Code {
			t.Fatalf("line %d changed: %+v", i, doc.Lines[i])
		}
	}
	note := doc.Lines[4]
	if note.Text != "mermaid: not drawn (parse error)" || !note.Decor || !note.Pieces[0].Style.Italic {
		t.Fatalf("note %+v", note)
	}
	if strings.Contains(RenderText(doc), "not drawn") {
		t.Fatal("the note is copied")
	}
}

func TestMermaidLookupOnlyForClosedMermaidFencesInRenderedMode(t *testing.T) {
	for name, text := range map[string]string{
		"other language": "```go\nx\n```",
		"unclosed":       "```mermaid\n" + diagramSource,
		"info words":     "```mermaid extra\n" + diagramSource + "```",
	} {
		var lookups []string
		Parse(text, diagramOptions(&lookups, Diagram{Lines: drawingRows}))
		if len(lookups) != 0 {
			t.Fatalf("%s: looked up %q", name, lookups)
		}
	}
	var lookups []string
	options := diagramOptions(&lookups, Diagram{Lines: drawingRows})
	options.Source = true
	doc := Parse("```mermaid\n"+diagramSource+"```", options)
	if len(lookups) != 0 || doc.Lines[1].Text != "flowchart LR" {
		t.Fatalf("source mode drew a diagram: %q %+v", lookups, doc.Lines)
	}
}

func TestMermaidDrawingQuotesTheWholeFenceOrNothing(t *testing.T) {
	var lookups []string
	options := diagramOptions(&lookups, Diagram{Lines: drawingRows})
	options.MapSource = true
	text := "intro\n```mermaid\n" + diagramSource + "```\nafter"
	doc := Parse(text, options)
	if len(doc.Sources) != len(doc.Lines) {
		t.Fatalf("%d sources for %d lines", len(doc.Sources), len(doc.Lines))
	}
	selection := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: text, Render: func(s string) string {
		return RenderText(Parse(s, options))
	}}
	last := len(drawingRows[2])
	fence := "```mermaid\n" + diagramSource + "```"
	// The whole drawing quotes the exact fence.
	if got, ok := selection.Exact(2, 0, 4, last, true, true); !ok || got != fence {
		t.Fatalf("whole drawing quoted %q %v", got, ok)
	}
	// With prose around it, still exact.
	if got, ok := selection.Exact(0, 0, 6, len("after"), true, true); !ok || got != text {
		t.Fatalf("drawing with prose quoted %q %v", got, ok)
	}
	// Part of the drawing is inexact: Quote refuses.
	for _, c := range [][4]int{{2, 3, 4, last}, {2, 0, 3, 4}, {0, 2, 3, 2}} {
		if got, ok := selection.Exact(c[0], c[1], c[2], c[3], true, true); ok {
			t.Fatalf("partial drawing %v quoted %q", c, got)
		}
	}
}

// Parsing with drawings, notes and misses keeps one in-bounds source per
// display line, and mapping, Quote and layout never panic.
func FuzzDiagramSourceMap(f *testing.F) {
	f.Add("intro\n```mermaid\nflowchart LR\n A --> B\n```\nafter", uint8(2))
	f.Add("```mermaid\n```\n```mermaid\nx", uint8(0))
	f.Add("| a | b |\n|---|---|\n```Mermaid\n a\n```\n```mermaid\nb\n```", uint8(5))
	f.Add("> q\n~~~mermaid\n\tx\n~~~\n- item", uint8(3))
	f.Fuzz(func(t *testing.T, text string, rows uint8) {
		if len(text) > 4096 {
			return
		}
		calls := 0
		options := documentOptions()
		options.MapSource = true
		options.Diagrams = func(string) (Diagram, bool) {
			calls++
			switch calls % 3 {
			case 0:
				return Diagram{Note: "mermaid: not drawn (x)"}, true
			case 1:
				lines := make([]string, int(rows)%7)
				for i := range lines {
					lines[i] = strings.Repeat("─", i) + "│"
				}
				return Diagram{Lines: lines}, true
			}
			return Diagram{}, false
		}
		doc := Parse(text, options)
		if len(doc.Sources) != len(doc.Lines) {
			t.Fatalf("%d sources for %d lines", len(doc.Sources), len(doc.Lines))
		}
		plain := Plain(text)
		for i, ls := range doc.Sources {
			if ls.Start < 0 || ls.Start > ls.End || ls.End > len(plain) {
				t.Fatalf("line %d source [%d,%d) of %d bytes", i, ls.Start, ls.End, len(plain))
			}
			for o := 0; o <= len(doc.Lines[i].Text); o++ {
				LineStart(ls, o)
				LineEnd(ls, o)
			}
		}
		if n := len(doc.Lines); n > 0 {
			sel := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: plain, Render: func(s string) string {
				return RenderText(Parse(s, documentOptions()))
			}}
			sel.Exact(0, 0, n-1, len(doc.Lines[n-1].Text), true, true)
			sel.Exact(n/2, 0, n-1, 0, true, false)
		}
		Layout(doc, 40, LayoutOptions{Method: options.Method, Theme: options.Theme})
	})
}
