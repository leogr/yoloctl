package chatui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func confirmingLinkModel() (*Model, *[]string) {
	m, _ := readyModel()
	var opened []string
	m.opts.ConfirmLinks = true
	m.opts.OpenLink = func(url string) tea.Cmd { opened = append(opened, url); return nil }
	m.Update(IncomingMsg{Message: Message{ID: "link", Text: "https://example.org/first"}})
	_ = m.SetDraft("preserved draft")
	return m, &opened
}

func TestLinkConfirmationFreezesDestinationAndPreservesInput(t *testing.T) {
	m, opened := confirmingLinkModel()
	press(m, 'o', tea.ModCtrl)
	if m.linkPrompt == nil || len(*opened) != 0 {
		t.Fatal("first Ctrl+O must only ask")
	}
	view := m.View()
	if !strings.Contains(ansi.Strip(view.Content), "https://example.org/first") || view.Cursor != nil || !view.KeyboardEnhancements.ReportEventTypes {
		t.Fatal("prompt must display its fixed destination, hide draft cursor and request repeat metadata")
	}
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl, IsRepeat: true}),
		tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyExtended, Text: "ctrl+o"}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}),
		tea.PasteMsg{Content: "ctrl+o\n"},
		tea.PasteStartMsg{},
		tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl}),
		tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}),
		tea.PasteEndMsg{},
	} {
		_, cmd := m.Update(msg)
		if cmd != nil || m.linkPrompt == nil || len(*opened) != 0 || m.Draft() != "preserved draft" {
			t.Fatalf("modal input escaped or approved: %#v", msg)
		}
	}
	// Async room state and history remain live, even if selection changes.
	m.Update(IncomingMsg{Message: Message{ID: "new", Text: "https://example.org/second"}})
	m.Update(IncomingMsg{Message: Message{ID: "old", Older: true, Text: "older history"}})
	m.Update(HistoryPageMsg{Count: 1})
	m.Update(MembersMsg{Members: []Member{{Nick: "peer"}}})
	m.selected = len(m.messages) - 1
	press(m, 'o', tea.ModCtrl)
	if m.linkPrompt != nil || len(*opened) != 1 || (*opened)[0] != "https://example.org/first" || len(m.messages) != 3 || len(m.members) != 1 {
		t.Fatalf("confirmation followed mutable selection or blocked room updates: %v", *opened)
	}
	if m.Draft() != "preserved draft" {
		t.Fatal("confirmation edited draft")
	}
}

func TestLinkConfirmationCancelAndDisconnect(t *testing.T) {
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}),
		tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}),
		ConnectionMsg{Err: errors.New("closed")},
	} {
		m, opened := confirmingLinkModel()
		press(m, 'o', tea.ModCtrl)
		m.Update(msg)
		if m.linkPrompt != nil || len(*opened) != 0 || m.Draft() != "preserved draft" {
			t.Fatalf("prompt survived cancellation or changed draft: %#v", msg)
		}
	}
}

func TestLinkConfirmationRendersBoundedAndRefusesHiddenPrompt(t *testing.T) {
	for _, size := range [][2]int{{20, 7}, {40, 10}, {80, 24}} {
		m, opened := confirmingLinkModel()
		m.messages[0].Text = "https://example.org/" + strings.Repeat("segment/", 100)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		press(m, 'o', tea.ModCtrl)
		for range 1000 {
			press(m, tea.KeyDown, 0)
		}
		rows := strings.Split(m.View().Content, "\n")
		if len(rows) != size[1] {
			t.Fatalf("prompt rows %d, expected %d", len(rows), size[1])
		}
		for _, row := range rows {
			if ansi.StringWidth(row) != size[0] {
				t.Fatalf("prompt row does not fit: %q", row)
			}
		}
		if !strings.Contains(ansi.Strip(rows[len(rows)-2]), "Esc") {
			t.Fatal("small prompt hid cancellation footer")
		}
		m.Update(tea.WindowSizeMsg{Width: 10, Height: 3})
		press(m, 'o', tea.ModCtrl)
		if len(*opened) != 0 || m.linkPrompt == nil {
			t.Fatal("approved destination while hidden by resize notice")
		}
	}
}
