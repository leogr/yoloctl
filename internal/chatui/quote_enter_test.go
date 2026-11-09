package chatui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// enterQuoteModel is quoteModel with the copy recorder: an older message
// from opus, then qa's message q, the latest.
func enterQuoteModel(t *testing.T) (*Model, *[]string) {
	t.Helper()
	m, copies, _ := filterModel(t)
	arrive(m, refTarget, "mo", "opus", "a loaded target")
	arrive(m, "q", "mq", "qa", "tests pass\nsmoke pending")
	m.View()
	return m, copies
}

// dragOver highlights part of display line line of message id with a real
// press, motion and release, as a person selects it.
func dragOver(t *testing.T, m *Model, id string, line int, part string) {
	t.Helper()
	key := "server:" + id
	i := m.indexOf(key)
	text := m.body(&m.messages[i], 80).lines[line].Text
	at := strings.Index(text, part)
	if at < 0 {
		t.Fatalf("line %d of %s is %q, without %q", line, id, text, part)
	}
	m.View()
	x0, y0 := cellOf(t, m, key, line, at)
	x1, y1 := cellOf(t, m, key, line, at+len(part))
	drag(m, x0, y0, x1, y1)
}

func actionIDs(m *Model) []string {
	var ids []string
	for _, a := range m.messageActions() {
		ids = append(ids, a.id)
	}
	return ids
}

func firstActions(t *testing.T, m *Model, want ...string) {
	t.Helper()
	ids := actionIDs(m)
	for i, id := range want {
		if i >= len(ids) || ids[i] != id {
			t.Fatalf("actions %v, want them to start with %v", ids, want)
		}
	}
}

// With a highlight in the picked message, Quote is the first action and
// Enter quotes it (leogr's case); without one, Enter replies, as before.
func TestEnterQuotesAHighlightInThePickedMessage(t *testing.T) {
	m, copies := enterQuoteModel(t)
	m.pick("server:q")
	firstActions(t, m, "reply", "copy")
	dragOver(t, m, "q", 1, "smoke pending")
	if len(*copies) != 1 || !m.picked || m.focus != focusMessages || m.actionIndex != 0 {
		t.Fatalf("after the drag: copies %q picked %v focus %v index %d", *copies, m.picked, m.focus, m.actionIndex)
	}
	firstActions(t, m, "quote", "reply", "copy")
	press(m, tea.KeyEnter, 0)
	if m.Draft() != "> smoke pending\n\n" || m.replyTo != "q" || m.sel != nil || len(*copies) != 1 {
		t.Fatalf("Enter: draft %q reply %q sel %v copies %q", m.Draft(), m.replyTo, m.sel, *copies)
	}

	// No highlight: Enter replies without quoting.
	m2, _ := enterQuoteModel(t)
	m2.pick("server:q")
	press(m2, tea.KeyEnter, 0)
	if m2.Draft() != "" || m2.replyTo != "q" {
		t.Fatalf("no highlight: draft %q reply %q", m2.Draft(), m2.replyTo)
	}
}

// A drag release returns the cursor to the first action even when nothing
// is copied: copy on select off and seams.clipboard off still quote.
func TestEnterQuotesWithoutCopying(t *testing.T) {
	m, copies := enterQuoteModel(t)
	m.opts.Clipboard, m.copyOnSelect = "off", false
	m.pick("server:q")
	press(m, tea.KeyRight, 0)
	press(m, tea.KeyRight, 0) // the cursor leaves Reply
	dragOver(t, m, "q", 0, "pass")
	if m.actionIndex != 0 {
		t.Fatalf("index %d after the drag", m.actionIndex)
	}
	press(m, tea.KeyEnter, 0)
	if m.Draft() != "> pass\n\n" || m.replyTo != "q" || len(*copies) != 0 {
		t.Fatalf("draft %q reply %q copies %q", m.Draft(), m.replyTo, *copies)
	}
}

// Esc that clears the highlight keeps the pick and brings Enter back to
// Reply, wherever the cursor was; Esc with message focus still drops the
// pick and keeps the highlight.
func TestEscClearingTheHighlightReturnsEnterToReply(t *testing.T) {
	m, _ := enterQuoteModel(t)
	m.pick("server:q")
	dragOver(t, m, "q", 1, "smoke")
	press(m, tea.KeyRight, 0)
	press(m, tea.KeyRight, 0) // Copy
	m.focus = focusComposer   // typing returned to the composer; the pick stays
	press(m, tea.KeyEscape, 0)
	if m.sel != nil || !m.picked || m.actionIndex != 0 {
		t.Fatalf("Esc: sel %v picked %v index %d", m.sel, m.picked, m.actionIndex)
	}
	firstActions(t, m, "reply", "copy")
	m.focus = focusMessages
	press(m, tea.KeyEnter, 0)
	if m.Draft() != "" || m.replyTo != "q" {
		t.Fatalf("Enter after Esc: draft %q reply %q", m.Draft(), m.replyTo)
	}

	m2, _ := enterQuoteModel(t)
	m2.pick("server:q")
	dragOver(t, m2, "q", 1, "smoke")
	press(m2, tea.KeyEscape, 0)
	if m2.picked || m2.focus != focusComposer || m2.sel == nil {
		t.Fatalf("message-focus Esc: picked %v focus %v sel %v", m2.picked, m2.focus, m2.sel)
	}
}

// Chip clicks map by id, so with Quote first a Reply click still replies
// without quoting and leaves the cursor on Reply.
func TestReplyChipClickWhileQuoteLeads(t *testing.T) {
	m, _ := enterQuoteModel(t)
	m.pick("server:q")
	dragOver(t, m, "q", 1, "smoke")
	m.View()
	h, ok := hitWith(m, hitFooter, "reply")
	if !ok {
		t.Fatalf("no Reply chip:\n%s", view(m))
	}
	clickAt(m, h.x0, h.y)
	if m.Draft() != "" || m.replyTo != "q" || m.actionIndex != 1 || m.sel == nil {
		t.Fatalf("Reply click: draft %q reply %q index %d sel %v", m.Draft(), m.replyTo, m.actionIndex, m.sel)
	}
}

// Alt+↑ picks with the cursor on the first action: Quote only when the
// highlight lies in the newly picked message.
func TestAltUpPickStartsOnQuoteOnlyForItsOwnHighlight(t *testing.T) {
	m, _ := enterQuoteModel(t)
	dragOver(t, m, refTarget, 0, "loaded")
	m.focus = focusComposer
	press(m, tea.KeyUp, tea.ModAlt) // from the latest, picks the older message
	if m.selectedMessage() == nil || m.selectedMessage().ID != refTarget || m.actionIndex != 0 {
		t.Fatalf("Alt+↑ picked %+v index %d", m.selectedMessage(), m.actionIndex)
	}
	firstActions(t, m, "quote", "reply")
	press(m, tea.KeyDown, 0) // q, without the highlight
	if m.selectedMessage().ID != "q" || m.actionIndex != 0 {
		t.Fatalf("↓ picked %+v index %d", m.selectedMessage(), m.actionIndex)
	}
	firstActions(t, m, "reply")
	press(m, tea.KeyEnter, 0)
	if m.Draft() != "" || m.replyTo != "q" {
		t.Fatalf("Enter on q: draft %q reply %q", m.Draft(), m.replyTo)
	}
}
