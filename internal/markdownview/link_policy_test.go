package markdownview

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/tui/spaceview"
)

func TestAdmittedLinksRenderLabelsWithOriginalSource(t *testing.T) {
	policy := func(s string) Action {
		if s == "a.md#x" {
			return Action{Kind: IntentLocalFile, Value: s}
		}
		if s == "https://example.com" {
			return Action{Kind: IntentURL, Value: s}
		}
		return Action{}
	}
	raw := "[label](a.md#x) [web](https://example.com) [missing](missing.md)"
	opts := ParseOptions{Theme: spaceview.NewTheme(true, nil, nil), MapSource: true, Links: policy}
	doc := Parse(raw, opts)
	if got := RenderText(doc); got != "label web missing (missing.md)" {
		t.Fatal(got)
	}
	if doc.Lines[0].Pieces[0].Act.Kind != IntentLocalFile || doc.Lines[0].Pieces[0].Act.Value != "a.md#x" {
		t.Fatal(doc.Lines[0].Pieces)
	}
	selection := SourceSelection{Lines: doc.Lines, Sources: doc.Sources, Text: raw, Render: func(s string) string { return RenderText(Parse(s, opts)) }}
	got, ok := selection.Exact(0, 0, 0, 5, true, true)
	if !ok || got != "[label](a.md#x)" {
		t.Fatalf("quote=%q exact=%v", got, ok)
	}
	table := Parse("| link | other |\n|---|---|\n| [label](a.md#x) | [missing](missing.md) |", opts)
	if text := RenderText(table); strings.Contains(text, "a.md") || !strings.Contains(text, "missing.md") {
		t.Fatal(text)
	}
	layout := Layout(table, 14, LayoutOptions{Method: ansi.WcWidth, Theme: opts.Theme})
	found := false
	for _, r := range layout.Rows {
		for _, v := range r.Runs {
			found = found || v.Act.Kind == IntentLocalFile
		}
	}
	if !found {
		t.Fatal("wrapped table lost local action")
	}
	opts.Source = true
	if RenderText(Parse(raw, opts)) != raw {
		t.Fatal("source changed")
	}
}
