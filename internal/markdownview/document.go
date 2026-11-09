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
	IntentNone     ActionKind = 0
	IntentURL      ActionKind = 1
	IntentCopyCode ActionKind = 7
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
}

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
	doc.Lines = parseLines(options.Method, text, options.Source, options.Theme, options.Decorate, sources)
	return doc
}

type LayoutOptions struct {
	Method ansi.Method
	Theme  spaceview.Theme
}

type LayoutResult struct {
	Rows    []BodyRow
	Limited bool // the existing display-row-limit notice was emitted
}

func Layout(doc Document, width int, options LayoutOptions) LayoutResult {
	var result LayoutResult
	result.Rows = bodyRows(options.Method, doc.Lines, width, options.Theme, &result.Limited)
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
