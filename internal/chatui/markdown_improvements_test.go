package chatui

import (
	"strings"
	"testing"
)

// A drag over a wrapped, padded ordered list copies the displayed item text
// without number padding or continuation gutters; Quote takes the source.
func TestListDragCopyAndQuote(t *testing.T) {
	key := "server:l"
	text := "9. alpha\n10. one two three four five six seven eight nine ten"
	m, copied := copyingModel(30, 30)
	m.Update(IncomingMsg{Message: Message{ID: "l", Nick: "x", Text: text}})
	m.View()
	lines := m.body(&m.messages[m.indexOf(key)], max(m.lastTextWidth, 1)).lines
	if lines[0].List.Pad != 1 || lines[1].List.Pad != 0 {
		t.Fatalf("pads %+v %+v", lines[0].List, lines[1].List)
	}
	x0, y0 := cellOf(t, m, key, 0, 0)
	x1, y1 := cellOf(t, m, key, 1, len(lines[1].Text))
	if ten, _ := cellOf(t, m, key, 1, 0); x0 != ten+1 || y1-y0 < 2 {
		t.Fatalf("the number should be padded and the item wrapped: %d,%d %d %d,%d", x0, y0, ten, x1, y1)
	}
	// Every continuation row starts under the item text, after "10. ".
	one, _ := cellOf(t, m, key, 1, len("10. "))
	for _, row := range m.lastTranscript {
		if row.key == key && row.line == 1 && row.offset > 0 {
			if x, _ := cellOf(t, m, key, 1, row.offset); x != one {
				t.Fatalf("continuation at %d, text at %d", x, one)
			}
		}
	}
	drag(m, x0, y0, x1, y1)
	if got := (*copied)[len(*copied)-1]; got != text {
		t.Fatalf("copy %q", got)
	}
	if got := quoted(m); got != "> 9. alpha\n> 10. one two three four five six seven eight nine ten\n\n" {
		t.Fatalf("quote %q (%q)", got, m.notice)
	}
}

// An angle autolink is the same chat link action as a bare URL, never
// extended over the text after it; Quote restores its brackets.
func TestAutolinkInChat(t *testing.T) {
	m := quoteModel(t, "see <https://example.org/x>foo and https://bare.example")
	dl := m.body(&m.messages[m.indexOf("server:q")], 80).lines[0]
	if dl.Text != "see https://example.org/xfoo and https://bare.example" {
		t.Fatalf("text %q", dl.Text)
	}
	links := map[string]string{}
	for _, p := range dl.Pieces {
		if p.Act.Kind == actLink {
			links[dl.Text[p.Start:p.End]] = p.Act.Value
		}
	}
	if len(links) != 2 || links["https://example.org/x"] != "https://example.org/x" || links["https://bare.example"] != "https://bare.example" {
		t.Fatalf("links %q", links)
	}
	highlightText(t, m, "q", 0, "https://example.org/x")
	if got := quoted(m); got != "> <https://example.org/x>\n\n" {
		t.Fatalf("quote %q (%q)", got, m.notice)
	}
}

// A rule is drawn, never copied, and Quote across it keeps the source line.
func TestRuleInChat(t *testing.T) {
	m := quoteModel(t, "above\n---\nbelow")
	lines := m.body(&m.messages[m.indexOf("server:q")], 80).lines
	if !lines[1].Rule || !lines[1].Decor {
		t.Fatalf("rule %+v", lines[1])
	}
	highlight(m, "q", 0, 0, 2, len("below"))
	if got := quoted(m); got != "> above\n> ---\n> below\n\n" {
		t.Fatalf("quote %q (%q)", got, m.notice)
	}
	if view := m.View().Content; !strings.Contains(view, "────") {
		t.Fatal("the rule is not drawn")
	}
}
