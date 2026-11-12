package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestLabelledTableKeepsChatCopyAndQuote(t *testing.T) {
	text := "| Name | Detail |\n|---|---|\n|alpha|" + strings.Repeat("detail ", 15) + "|"
	m := quoteModel(t, text)
	m.Update(tea.WindowSizeMsg{Width: 23, Height: 60})
	if !strings.Contains(ansi.Strip(m.View().Content), "Name: alpha") {
		t.Fatal("fixture did not label table")
	}
	dl := m.body(&m.messages[m.indexOf("server:q")], m.lastTextWidth).lines[2]
	highlight(m, "q", 2, 0, 2, len(dl.Text))
	if got, ok := m.selectedText(*m.sel); !ok || got != dl.CopyText(0, len(dl.Text)) {
		t.Fatalf("display copy changed: %q", got)
	}
	if got := quoted(m); got != "> alpha\t"+strings.TrimSpace(strings.Repeat("detail ", 15))+"\n\n" {
		t.Fatalf("quote labels/notice leaked: %q", got)
	}
	if m.Messages()[m.indexOf("server:q")].Text != text {
		t.Fatal("source message changed")
	}
}
