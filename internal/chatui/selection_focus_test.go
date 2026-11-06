package chatui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// pressAndMove presses at the start of part of display line line of message
// id and moves to its end, without releasing, so the state between the first
// motion and the release can be checked.
func pressAndMove(t *testing.T, m *Model, id string, line int, part string) (int, int) {
	t.Helper()
	key := "server:" + id
	i := m.indexOf(key)
	text := m.body(&m.messages[i], 80).lines[line].text
	at := -1
	for j := 0; j+len(part) <= len(text); j++ {
		if text[j:j+len(part)] == part {
			at = j
			break
		}
	}
	if at < 0 {
		t.Fatalf("line %d of %s is %q, without %q", line, id, text, part)
	}
	m.View()
	x0, y0 := cellOf(t, m, key, line, at)
	x1, y1 := cellOf(t, m, key, line, at+len(part)-1) // the last grapheme is selected inclusively
	m.Update(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	return x1, y1
}

// leogr's case: a selection that starts in a message selects that message
// at its first motion, so after the release Enter quotes the highlight at once.
func TestDragStartSelectsItsMessage(t *testing.T) {
	m, copies := enterQuoteModel(t)
	if m.picked {
		t.Fatal("setup: no message is picked")
	}
	x, y := pressAndMove(t, m, "q", 1, "smoke pending")
	if !m.picked || m.selectedMessage() != &m.messages[m.indexOf("server:q")] || m.focus != focusMessages || m.sel == nil {
		t.Fatalf("first motion: picked %v focus %v sel %v", m.picked, m.focus, m.sel)
	}
	m.View() // a render between motion and release keeps the highlight
	if m.sel == nil {
		t.Fatal("the render dropped the highlight")
	}
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if len(*copies) != 1 || (*copies)[0] != "smoke pending" {
		t.Fatalf("copy on release: %q", *copies)
	}
	firstActions(t, m, "quote", "reply", "copy")
	press(m, tea.KeyEnter, 0)
	if m.Draft() != "> smoke pending\n\n" || m.replyTo != "q" {
		t.Fatalf("Enter: draft %q reply %q", m.Draft(), m.replyTo)
	}
}

// A drag in another message moves the pick there.
func TestDragMovesThePickToItsMessage(t *testing.T) {
	m, _ := enterQuoteModel(t)
	m.pick("server:" + refTarget)
	dragOver(t, m, "q", 0, "tests pass")
	if m.selectedMessage() != &m.messages[m.indexOf("server:q")] || m.focus != focusMessages {
		t.Fatalf("the pick stayed on %v", m.selectedMessage())
	}
	firstActions(t, m, "quote", "reply")
}

// A selection spanning messages selects the one where it started; Quote is
// not offered for it, so Enter replies to that message.
func TestDragAcrossMessagesSelectsWhereItStarted(t *testing.T) {
	m, _ := enterQuoteModel(t)
	m.View()
	x0, y0 := cellOf(t, m, "server:"+refTarget, 0, 0)
	x1, y1 := cellOf(t, m, "server:q", 0, len("tests"))
	drag(m, x0, y0, x1, y1)
	if m.selectedMessage() != &m.messages[m.indexOf("server:"+refTarget)] || m.focus != focusMessages || m.sel == nil {
		t.Fatalf("picked %v focus %v", m.selectedMessage(), m.focus)
	}
	for _, id := range actionIDs(m) {
		if id == "quote" {
			t.Fatal("Quote offered for a highlight across messages")
		}
	}
	press(m, tea.KeyEnter, 0)
	if m.Draft() != "" || m.replyTo != refTarget {
		t.Fatalf("Enter: draft %q reply %q", m.Draft(), m.replyTo)
	}
}

// A plain click still picks on release, with Reply first.
func TestClickStillPicksOnRelease(t *testing.T) {
	m, copies := enterQuoteModel(t)
	m.View()
	x, y := cellOf(t, m, "server:q", 0, 2)
	m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.picked {
		t.Fatal("a press alone picked the message")
	}
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if !m.picked || m.selectedMessage() != &m.messages[m.indexOf("server:q")] || len(*copies) != 0 {
		t.Fatalf("click: picked %v copies %q", m.picked, *copies)
	}
	firstActions(t, m, "reply", "copy")
}

// Scrolled back (not following), picking at the first motion keeps the
// reading position: the highlighted text stays under the pointer.
func TestDragStartPickKeepsAScrolledPosition(t *testing.T) {
	m, copies := enterQuoteModel(t)
	for i := range 40 {
		arrive(m, fmt.Sprintf("n%02d", i), "mo", "opus", fmt.Sprintf("filler %02d", i))
	}
	m.View()
	for range 30 {
		m.Update(tea.MouseWheelMsg{X: 5, Y: m.layout.bodyTop + 1, Button: tea.MouseWheelUp})
	}
	m.View()
	if m.follow {
		t.Fatal("setup: the view still follows the latest message")
	}
	target := ""
	for _, row := range m.lastTranscript[m.scroll:min(m.scroll+m.layout.bodyHeight, len(m.lastTranscript))] {
		if row.kind == rowBody && row.key != "" && row.key != "server:q" {
			target = row.key
			break
		}
	}
	if target == "" {
		t.Fatal("setup: no message on screen")
	}
	id := m.messages[m.indexOf(target)].ID
	scroll := m.scroll
	x, y := pressAndMove(t, m, id, 0, "filler")
	m.View()
	if m.scroll != scroll || m.selectedMessage() != &m.messages[m.indexOf(target)] {
		t.Fatalf("scroll %d → %d, picked %v", scroll, m.scroll, m.selectedMessage())
	}
	m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if len(*copies) != 1 || (*copies)[0] != "filler" {
		t.Fatalf("copied %q", *copies)
	}
}

// A drag that starts on a room notice row (join, topic, ...) selects no
// message, although pointAt snaps it to the message it follows.
func TestDragFromARoomNoticeSelectsNoMessage(t *testing.T) {
	m, _ := enterQuoteModel(t)
	m.Update(TopicMsg{Text: "Ship it", By: "opus2", Live: true})
	m.View()
	y := -1
	for i, row := range m.lastTranscript {
		if row.kind == rowEvent && i >= m.scroll && i < m.scroll+m.layout.bodyHeight {
			y = i - m.scroll + m.layout.bodyTop
		}
	}
	if y < 0 {
		t.Fatal("setup: no room notice on screen")
	}
	drag(m, 4, y, 12, y)
	if m.picked {
		t.Fatalf("a drag from a room notice picked %v", m.selectedMessage())
	}
}
