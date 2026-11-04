package chatui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestTranscriptAnchorSurvivesLiveArrivalAndResize(t *testing.T) {
	m, _ := readyModel()
	m.Update(IncomingMsg{Message: Message{ID: "long", Text: strings.Repeat("界-code-review ", 150)}})
	m.Update(IncomingMsg{Message: Message{ID: "tail", Text: "latest"}})
	m.View()
	press(m, tea.KeyPgUp, 0)
	m.View()
	before := m.lastTranscript[m.scroll]
	if m.follow || before.key != "server:long" {
		t.Fatal("test reader did not move into older long content")
	}
	m.Update(IncomingMsg{Message: Message{ID: "new", Text: "new live text"}})
	m.View()
	if after := m.lastTranscript[m.scroll]; after.key != before.key || after.line != before.line || after.offset != before.offset || m.unread != 1 {
		t.Fatal("arrival moved the reader or lost unread")
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	m.View()
	after := m.lastTranscript[m.scroll]
	if after.key != before.key || after.line != before.line || after.offset > before.offset {
		t.Fatalf("resize lost logical anchor: before=%+v, after=%+v", before, after)
	}
	if m.scroll+1 < len(m.lastTranscript) {
		next := m.lastTranscript[m.scroll+1]
		if next.key == before.key && next.line == before.line && next.offset <= before.offset {
			t.Fatal("resize chose an earlier row instead of the row containing the anchor")
		}
	}
	press(m, tea.KeyEnd, tea.ModAlt)
	m.View()
	if !m.follow || m.unread != 0 {
		t.Fatal("jump to live did not clear unread")
	}
}

func TestMarkdownSourceToggleAndExplicitRenderLimit(t *testing.T) {
	m, _ := readyModel()
	raw := "# Heading\n> quotation\n```go\n  keep indentation\n```"
	m.Update(IncomingMsg{Message: Message{ID: "one", Text: raw}})
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, " go ") || !strings.Contains(view, "  keep indentation") || strings.Contains(view, "```go") {
		t.Fatal("bounded Markdown block display is missing")
	}
	press(m, 's', tea.ModCtrl)
	view = ansi.Strip(m.View().Content)
	if !strings.Contains(view, "# Heading") || !strings.Contains(view, "```go") || m.Messages()[0].Text != raw {
		t.Fatal("source toggle changed or lost original Markdown")
	}
	long := strings.Repeat("line\n", 20000)
	m.Update(IncomingMsg{Message: Message{ID: "long", Text: long}})
	view = ansi.Strip(m.View().Content)
	if !strings.Contains(view, "display row limit") || m.Messages()[1].Text != long {
		t.Fatal("extreme rendering was silently truncated or rewrote original text")
	}
}

func TestWrappedUnicodeVerticalEditing(t *testing.T) {
	m, _ := readyModel()
	m.Update(tea.WindowSizeMsg{Width: 21, Height: 10}) // composer content width 16
	paste(m, "a界bcdefghijklmnabc")
	press(m, tea.KeyUp, 0)
	press(m, tea.KeyBackspace, 0)
	if m.Draft() != "abcdefghijklmnabc" {
		t.Fatalf("vertical editing did not use rendered columns/graphemes: %q", m.Draft())
	}
	if err := m.SetDraft(strings.Repeat("\n", maxDraftLines)); err == nil {
		t.Fatal("unbounded newline paste was accepted")
	}
}

func TestVerticalMovementPreservesPreferredColumn(t *testing.T) {
	m, _ := readyModel()
	paste(m, "12345\nx\nabcde")
	press(m, tea.KeyUp, 0)
	press(m, tea.KeyUp, 0)
	press(m, tea.KeyBackspace, 0)
	if m.Draft() != "1234\nx\nabcde" {
		t.Fatalf("short middle line lost preferred column: %q", m.Draft())
	}
}

func TestReceiptAboveViewportPreservesReadingAnchor(t *testing.T) {
	m, sends := readyModel()
	paste(m, "pending")
	press(m, tea.KeyEnter, 0)
	for i := range 20 {
		m.Update(IncomingMsg{Message: Message{ID: fmt.Sprint(i), Text: "body"}})
	}
	m.View()
	press(m, tea.KeyPgUp, 0)
	m.View()
	before := m.lastTranscript[m.scroll]
	m.Update(DeliveryMsg{ID: (*sends)[0].ID, State: Rejected, Err: errors.New("server rejected")})
	m.View()
	if after := m.lastTranscript[m.scroll]; after.key != before.key || after.line != before.line || after.offset != before.offset {
		t.Fatalf("receipt above reader moved viewport: before=%+v after=%+v", before, after)
	}
}

func TestHostActionResultsDoNotChangeDelivery(t *testing.T) {
	m, sends := readyModel()
	paste(m, "pending")
	press(m, tea.KeyEnter, 0)
	m.Update(ActionResultMsg{Text: "copied"})
	if m.notice != "copied" || m.Draft() != "pending" || m.Messages()[0].Delivery != Pending || len(*sends) != 1 {
		t.Fatal("copy callback result was confused with a send receipt")
	}
}
