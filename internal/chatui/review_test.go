package chatui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Regression scenarios for retained reading anchors, reply intent and hard
// line-end cursors, supplied by independent source review.
func TestQAHistoryEvictionPreservesRetainedReadingAnchor(t *testing.T) {
	m, _ := readyModel()
	for i := range maxMessages {
		m.Update(IncomingMsg{Message: Message{ID: fmt.Sprintf("m-%03d", i), Text: "body"}})
	}
	m.View()
	press(m, tea.KeyPgUp, 0)
	m.View()
	before := m.lastTranscript[m.scroll]
	if m.follow || before.key == "server:m-000" {
		t.Fatalf("fixture did not establish a retained non-live anchor: %+v", before)
	}
	m.Update(IncomingMsg{Message: Message{ID: "m-new", Text: "arrival evicts only m-000"}})
	m.View()
	after := m.lastTranscript[m.scroll]
	if after.key != before.key || after.line != before.line || after.offset != before.offset {
		t.Fatalf("unrelated oldest-message eviction moved the reader: before=%+v after=%+v", before, after)
	}
}

func TestQALateConfirmationPreservesNewReplySelection(t *testing.T) {
	for _, viaEcho := range []bool{false, true} {
		t.Run(fmt.Sprintf("echo-%v", viaEcho), func(t *testing.T) {
			m, sends := readyModel()
			m.Update(IncomingMsg{Message: Message{ID: "target-a", Text: "first"}})
			m.Update(IncomingMsg{Message: Message{ID: "target-b", Text: "second"}})
			press(m, 'r', tea.ModCtrl) // reply to selected target-b
			paste(m, "reply body")
			press(m, tea.KeyEnter, 0)
			out := (*sends)[0]
			if out.ReplyTo != "target-b" {
				t.Fatal("fixture did not submit the original reply target")
			}
			press(m, tea.KeyUp, tea.ModAlt) // pending -> target-b
			press(m, tea.KeyUp, tea.ModAlt) // target-b -> target-a
			press(m, 'r', tea.ModCtrl)
			if m.replyTo != "target-a" {
				t.Fatal("fixture did not establish a new reply selection")
			}
			if viaEcho {
				m.Update(IncomingMsg{Message: Message{ID: "sent", ClientID: out.ID, Text: out.Text, ReplyTo: out.ReplyTo}})
			} else {
				m.Update(DeliveryMsg{ID: out.ID, MessageID: "sent", State: Confirmed})
			}
			if m.replyTo != "target-a" {
				t.Fatalf("late confirmation discarded the newer reply selection: %q", m.replyTo)
			}
		})
	}
}

func TestQAHardLineEndCursorFollowsFinalGrapheme(t *testing.T) {
	for _, first := range []string{"0123456789ABCDEF", "界0123456789ABCD"} {
		t.Run(first, func(t *testing.T) {
			m, _ := readyModel()
			m.Update(tea.WindowSizeMsg{Width: 21, Height: 10}) // composer content width 16
			paste(m, first+"\nx")
			press(m, tea.KeyHome, tea.ModCtrl)
			press(m, 'e', tea.ModCtrl)
			if m.editor.cursor != len(first) {
				t.Fatal("fixture cursor is not at first logical EOL")
			}
			view := m.View()
			// The line occupies cells 4..19 and the renderer pads cell 20.
			// A cursor at logical EOL belongs after the last displayed grapheme.
			if view.Cursor == nil || view.Cursor.X != 20 {
				t.Fatalf("logical EOL cursor is painted on the final grapheme: %+v", view.Cursor)
			}
		})
	}
}
