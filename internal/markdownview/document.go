// Package markdownview parses and lays out the existing bounded Markdown
// subset. It has no transport, model, cache, clipboard or opener authority.
package markdownview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

// Action kinds other than the two shared intents are opaque caller IDs. The
// payload strings are also opaque to layout; no action is executed here.
const (
	IntentNone           ActionKind = 0
	IntentURL            ActionKind = 1
	IntentCopyCode       ActionKind = 7
	IntentToggleTableRow ActionKind = 8
)

// Decorator supplies caller-specific styles and opaque action IDs. It is
// synchronous and pure; Code pieces must retain their literal styling.
type Decorator func(text string, pieces []Piece, theme spaceview.Theme, prose bool) []Piece

type ParseOptions struct {
	Method    ansi.Method
	Theme     spaceview.Theme
	Source    bool
	MapSource bool
	Decorate  Decorator
	// Diagrams looks up a closed ```mermaid fence's drawing by its source
	// (the opener's Block). It must not block or do I/O: the caller answers
	// from its cache, for DiagramWidth of the layout width it will use.
	Diagrams func(source string) (Diagram, bool)
}

// Diagram is what the caller has for a mermaid fence: drawing rows (shown in
// place of its code, copyable, never re-wrapped), or none and a note shown as
// one row after the fence (never copied): a fallback reason or "drawing…".
type Diagram struct {
	Lines []string
	Note  string
}

// DiagramWidth is the inner width of a code card in a layout of width:
// the width a drawing must be requested for so its rows never wrap.
func DiagramWidth(width int) int { return max(width-2*CardPad(width), 1) }

// IsMermaid reports whether a fence opener's info string selects Mermaid.
func IsMermaid(info string) bool { return strings.EqualFold(strings.TrimSpace(info), "mermaid") }

type Document struct {
	Lines   []DisplayLine
	Sources []LineSource // nil unless MapSource was requested
}

func Parse(text string, options ParseOptions) Document {
	var doc Document
	var sources *[]LineSource
	if options.MapSource {
		sources = &doc.Sources
	}
	var diagrams func(string) (Diagram, bool)
	if !options.Source {
		diagrams = options.Diagrams
	}
	doc.Lines = parseLines(options.Method, text, options.Source, options.Theme, options.Decorate, sources, diagrams)
	return doc
}

type LayoutOptions struct {
	Method ansi.Method
	Theme  spaceview.Theme
	// UnlimitedRows is for complete file documents; chat keeps its default bound.
	UnlimitedRows bool
	CompactTables bool
	OpenTableRows map[TableRowID]bool
}

type LayoutResult struct {
	Rows             []BodyRow
	RefusedTableRows []TableRowID
	Limited          bool // the existing display-row-limit notice was emitted
}

func Layout(doc Document, width int, options LayoutOptions) LayoutResult {
	var result LayoutResult
	result.Rows = bodyRows(options.Method, doc.Lines, width, options.Theme, &result.Limited, options, &result.RefusedTableRows)
	return result
}

// RenderText is logical display-copy text, without frames and row wrapping.
func RenderText(doc Document) string {
	var lines []string
	for _, line := range doc.Lines {
		if !line.Decor {
			lines = append(lines, line.Text)
		}
	}
	return strings.Join(lines, "\n")
}

// URLAt recognizes exactly the existing bare http(s) token at byte i.
func URLAt(text string, i int) (end int, link string, ok bool) {
	if !strings.HasPrefix(text[i:], "https://") && !strings.HasPrefix(text[i:], "http://") {
		return 0, "", false
	}
	end = i + strings.IndexAny(text[i:]+" ", " \t")
	link = strings.TrimRight(text[i:end], ").,;:]>\"'")
	return end, link, true
}

func decorateTokens(text string, pieces []Piece, theme spaceview.Theme, decorate Decorator, prose bool) []Piece {
	if decorate != nil {
		return decorate(text, pieces, theme, prose)
	}
	for i := 0; i < len(text); {
		if end, link, ok := URLAt(text, i); ok {
			pieces = Overlay(pieces, i, i+len(link), func(p Piece) Piece {
				p.Style.FG, p.Style.Underline = theme.Label, true
				p.Act = Action{Kind: IntentURL, Value: link}
				return p
			})
			i = end
		} else {
			i++
		}
	}
	return pieces
}
